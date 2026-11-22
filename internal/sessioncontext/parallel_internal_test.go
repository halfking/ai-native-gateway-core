package sessioncontext

// runIndexed 自身的语义测试（内部包：直接调未导出函数）。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRunIndexedSemantics(t *testing.T) {
	ctx := context.Background()
	t.Run("全部成功", func(t *testing.T) {
		got := make([]int, 50)
		idx, err := runIndexed(ctx, 8, 50,
			func(i int) (any, error) { return i * 2, nil },
			func(i int, v any) error { got[i] = v.(int) + 1; return nil })
		if idx != -1 || err != nil {
			t.Fatalf("idx=%d err=%v", idx, err)
		}
		for i, v := range got {
			if v != i*2+1 {
				t.Fatalf("slot %d = %d", i, v)
			}
		}
	})
	t.Run("返回下标最小的错误且不再装载", func(t *testing.T) {
		loaded := 0
		idx, err := runIndexed(ctx, 4, 10_000,
			func(i int) (any, error) { loaded++; return i, nil },
			func(i int, _ any) error {
				if i == 30 || i == 900 {
					if i == 30 {
						// 出错的这条故意慢：窗口若无序，别的协程在这段时间里
						// 能循环装载成百上千条；有序窗口则装满窗口就卡住等它。
						time.Sleep(20 * time.Millisecond)
					}
					return fmt.Errorf("fail@%d", i)
				}
				return nil
			})
		if idx != 30 || err == nil || err.Error() != "fail@30" {
			t.Fatalf("idx=%d err=%v, want 30 / fail@30", idx, err)
		}
		// 上界是结构保证的（有序装载窗口，出错条目先置失败再发完成信号）：
		// 下标 30 出错后至多装载到下标 30+window−1。窗口无序时这里能到九百多
		//（2026-11-23 实测 902~906）。
		if bound := 30 + loadWindow(4); loaded > bound {
			t.Fatalf("出错后仍在装载：loaded=%d > %d", loaded, bound)
		}
	})
	// ★ 相邻条目同时出错（都在在途窗口里）：这才是「最小下标」规则真正起作用的
	// 场景——第一条出错后装载就停了，离得远的条目根本不会被处理。
	t.Run("在途窗口内多条出错时返回最小下标", func(t *testing.T) {
		for rep := 0; rep < 50; rep++ {
			for _, workers := range []int{2, 3, 5, 8, 16} {
				idx, err := runIndexed(ctx, workers, 200,
					func(i int) (any, error) { return i, nil },
					func(i int, _ any) error {
						if i >= 30 && i <= 60 {
							return fmt.Errorf("fail@%d", i)
						}
						return nil
					})
				if idx != 30 || err == nil || err.Error() != "fail@30" {
					t.Fatalf("rep=%d workers=%d: idx=%d err=%v, want 30 / fail@30", rep, workers, idx, err)
				}
			}
		}
	})
	t.Run("装载出错", func(t *testing.T) {
		want := errors.New("load boom")
		idx, err := runIndexed(ctx, 8, 100,
			func(i int) (any, error) {
				if i == 9 {
					return nil, want
				}
				return i, nil
			},
			func(int, any) error { return nil })
		if idx != 9 || !errors.Is(err, want) {
			t.Fatalf("idx=%d err=%v", idx, err)
		}
	})
	t.Run("空输入与并行度大于条目数", func(t *testing.T) {
		if idx, err := runIndexed(ctx, 8, 0, func(int) (any, error) { t.Fatal("must not load"); return nil, nil }, func(int, any) error { return nil }); idx != -1 || err != nil {
			t.Fatalf("empty: idx=%d err=%v", idx, err)
		}
		if idx, err := runIndexed(ctx, 64, 3, func(i int) (any, error) { return i, nil }, func(int, any) error { return nil }); idx != -1 || err != nil {
			t.Fatalf("workers>n: idx=%d err=%v", idx, err)
		}
	})
	t.Run("投递时取消", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		idx, err := runIndexed(cctx, 4, 10,
			func(int) (any, error) { return 1, nil },
			func(int, any) error { return nil })
		if idx != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("idx=%d err=%v, want 0 / context.Canceled", idx, err)
		}
	})
	t.Run("并行度未声明时就是串行", func(t *testing.T) {
		if reduceWorkers(ctx) != 1 || reduceWorkers(WithReduceWorkers(ctx, 0)) != 1 || reduceWorkers(WithReduceWorkers(ctx, 6)) != 6 {
			t.Fatal("reduceWorkers 语义漂移")
		}
	})
}
