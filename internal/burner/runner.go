package burner

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"token-devastator/internal/config"
)

// 停止原因。
const (
	StopReasonCompleted           = "completed"            // 跑完所有輪次
	StopReasonManual              = "manual"               // 手動停止
	StopReasonConsecutiveFailures = "consecutive-failures" // 連續失敗達門檻
)

const (
	maxAttempts             = 5           // 單一邏輯請求的最大嘗試次數（1 次原始 + 4 次重試）
	consecutiveFailuresStop = 10          // 連續失敗門檻，達到即自動停止
	defaultBackoffBase      = time.Second // 指數退避基準
	maxBackoff              = 30 * time.Second
)

// Stats Runner 的對外統計快照。
type Stats struct {
	Running      bool      `json:"running"`
	StopReason   string    `json:"stopReason"`
	Round        int       `json:"round"`     // 當前（或最後）輪次，1 起
	RoundDone    int       `json:"roundDone"` // 已完成輪數
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	OK           int64     `json:"ok"`
	Failed       int64     `json:"failed"`
	Retries      int64     `json:"retries"`
	LastError    string    `json:"lastError"`
	StartedAt    time.Time `json:"startedAt"`
}

// Runner 管理單一 profile 的燒毀執行：輪次調度、並發、重試與統計。
type Runner struct {
	profile     config.Profile
	client      *Client
	backoffBase time.Duration

	mu           sync.Mutex
	running      bool
	stopReason   string
	round        int
	roundDone    int
	inputTokens  int64
	outputTokens int64
	ok           int64
	failed       int64
	retries      int64
	lastError    string
	startedAt    time.Time
	consecutive  int64

	cancel context.CancelFunc
}

// NewRunner 建立 Runner；profile 需通過驗證。
// backoffBase 為指數退避基準（測試可注入小值；0 表示預設 1 秒）。
func NewRunner(p config.Profile, hc *http.Client, backoffBase time.Duration) (*Runner, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if backoffBase <= 0 {
		backoffBase = defaultBackoffBase
	}
	return &Runner{
		profile:     p,
		client:      &Client{Profile: p, HTTP: hc},
		backoffBase: backoffBase,
	}, nil
}

// Start 啟動（或重新啟動）燒毀任務。重啟時統計歸零。
func (r *Runner) Start(parent context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("任務已在執行中")
	}
	r.running = true
	r.stopReason = ""
	r.round = 0
	r.roundDone = 0
	r.inputTokens = 0
	r.outputTokens = 0
	r.ok = 0
	r.failed = 0
	r.retries = 0
	r.lastError = ""
	r.startedAt = time.Now()
	r.consecutive = 0
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.mu.Unlock()

	go r.run(ctx)
	return nil
}

// Stop 手動停止。
func (r *Runner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	running := r.running
	if running && r.stopReason == "" {
		r.stopReason = StopReasonManual
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Stats 回傳當前統計快照。
func (r *Runner) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{
		Running:      r.running,
		StopReason:   r.stopReason,
		Round:        r.round,
		RoundDone:    r.roundDone,
		InputTokens:  r.inputTokens,
		OutputTokens: r.outputTokens,
		OK:           r.ok,
		Failed:       r.failed,
		Retries:      r.retries,
		LastError:    r.lastError,
		StartedAt:    r.startedAt,
	}
}

// Profile 回傳 runner 綁定的 profile 快照。
func (r *Runner) Profile() config.Profile {
	return r.profile
}

// run 主循環：逐輪執行，每輪開 Concurrency 個 worker，輪間屏障同步。
func (r *Runner) run(ctx context.Context) {
	defer func() {
		r.mu.Lock()
		r.running = false
		if r.stopReason == "" {
			r.stopReason = StopReasonCompleted
		}
		r.mu.Unlock()
	}()

	for round := 1; round <= r.profile.Rounds; round++ {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.round = round
		r.mu.Unlock()

		var wg sync.WaitGroup
		for i := 0; i < r.profile.Concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.runOnce(ctx)
			}()
		}
		wg.Wait()

		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.roundDone = round
		r.mu.Unlock()
	}
}

// runOnce 執行一次帶重試退避的燒毀請求。
func (r *Runner) runOnce(ctx context.Context) {
	prompt, maxTokens := BuildPrompt(r.profile)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		u, err := r.client.Do(ctx, prompt, maxTokens)
		if err == nil {
			r.recordSuccess(u)
			return
		}
		if ctx.Err() != nil {
			return // 任務中止，不記為失敗
		}
		var se *StatusError
		if errors.As(err, &se) && se.Retryable() && attempt < maxAttempts {
			r.mu.Lock()
			r.retries++
			r.mu.Unlock()
			if !sleep(ctx, r.backoff(attempt)) {
				return
			}
			continue
		}
		r.recordFailure(err)
		return
	}
}

func (r *Runner) recordSuccess(u Usage) {
	r.mu.Lock()
	r.ok++
	r.inputTokens += u.InputTokens
	r.outputTokens += u.OutputTokens
	r.consecutive = 0
	r.mu.Unlock()
}

func (r *Runner) recordFailure(err error) {
	r.mu.Lock()
	r.failed++
	r.consecutive++
	r.lastError = err.Error()
	over := r.consecutive >= consecutiveFailuresStop
	if over && r.stopReason == "" {
		r.stopReason = StopReasonConsecutiveFailures
	}
	cancel := r.cancel
	r.mu.Unlock()
	if over && cancel != nil {
		cancel() // 觸發全體中止
	}
}

// backoff 第 attempt 次失敗後的等待時間：基準 × 2^(attempt-1)，上限 30s。
func (r *Runner) backoff(attempt int) time.Duration {
	d := r.backoffBase
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// sleep 可被 ctx 中斷的等待；回傳 false 表示被中斷。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
