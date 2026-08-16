package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/jonbaldie/gastown/internal/krc"
)

// KRCPruner manages automatic pruning of expired ephemeral records.
// It runs as a background goroutine within the daemon.
type KRCPruner struct {
	townRoot string
	config   *krc.Config
	logger   func(format string, args ...interface{})
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewKRCPruner creates a new KRC pruner.
func NewKRCPruner(townRoot string, logger func(format string, args ...interface{})) (*KRCPruner, error) {
	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &KRCPruner{
		townRoot: townRoot,
		config:   config,
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
	}, nil
}

// Start begins the pruner goroutine.
func (p *KRCPruner) Start() error {
	// Run initial prune on startup
	p.prune()

	// Start periodic pruning
	p.wg.Add(1)
	go p.run()

	return nil
}

// Stop gracefully stops the pruner.
func (p *KRCPruner) Stop() {
	p.cancel()
	p.wg.Wait()
}

// run is the main pruner loop.
func (p *KRCPruner) run() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.PruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.prune()
		}
	}
}

// prune runs a single prune operation.
func (p *KRCPruner) prune() {
	pruner := krc.NewPruner(p.townRoot, p.config)
	result, err := pruner.Prune()
	if err != nil {
		p.logger("KRC prune error: %v", err)
		return
	}

	if result.EventsPruned > 0 {
		p.logger("KRC pruned %d events (saved %d bytes) in %v",
			result.EventsPruned,
			result.BytesBefore-result.BytesAfter,
			result.Duration.Round(time.Millisecond))
	}
}
