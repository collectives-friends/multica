package featureflag

import (
	"context"
	"sync"
	"testing"
)

func TestLayerReloadCannotPromoteTargetingToDeployment(t *testing.T) {
	provider := NewStaticProvider()
	service := NewService(provider)
	ctx := WithEvalContext(context.Background(), EvalContext{UserID: "user"})
	stop := make(chan struct{})
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			provider.Set("flag", Rule{Default: false})
			provider.Set("flag", Rule{Allow: []string{"user"}})
		}
	}()
	defer func() { close(stop); done.Wait() }()
	for i := 0; i < 100000; i++ {
		if d, ok := service.LookupLayer(ctx, "flag", true); ok && d.Enabled {
			t.Fatal("targeting rule evaluated as a deployment override")
		}
	}
}
