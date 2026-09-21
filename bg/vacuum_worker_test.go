package bg

import (
	"context"
	"testing"
	"time"
)

func TestVacuumWorker_SetInterval(t *testing.T) {
	w := NewVacuumWorker(nil)
	
	// 默认值
	if w.interval != 7*24*time.Hour {
		t.Errorf("default interval = %v, want %v", w.interval, 7*24*time.Hour)
	}
	
	// 设置新值
	w.SetInterval(1 * time.Hour)
	if w.interval != 1*time.Hour {
		t.Errorf("interval = %v, want %v", w.interval, 1*time.Hour)
	}
}

func TestVacuumWorker_SetExecuteHour(t *testing.T) {
	w := NewVacuumWorker(nil)
	
	// 默认值
	if w.executeHour != 2 {
		t.Errorf("default executeHour = %d, want 2", w.executeHour)
	}
	
	// 设置有效值
	w.SetExecuteHour(10)
	if w.executeHour != 10 {
		t.Errorf("executeHour = %d, want 10", w.executeHour)
	}
	
	// 边界值
	w.SetExecuteHour(0)
	if w.executeHour != 0 {
		t.Errorf("executeHour = %d, want 0", w.executeHour)
	}
	
	w.SetExecuteHour(23)
	if w.executeHour != 23 {
		t.Errorf("executeHour = %d, want 23", w.executeHour)
	}
	
	// 无效值不应改变
	w.SetExecuteHour(25)
	if w.executeHour != 23 {
		t.Errorf("executeHour = %d, want 23 (unchanged)", w.executeHour)
	}
	
	w.SetExecuteHour(-1)
	if w.executeHour != 23 {
		t.Errorf("executeHour = %d, want 23 (unchanged)", w.executeHour)
	}
}

func TestVacuumWorker_StartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping worker lifecycle test in short mode")
	}

	w := NewVacuumWorker(nil)
	
	ctx := context.Background()
	w.Start(ctx)
	
	// 给 worker 一点时间启动
	time.Sleep(100 * time.Millisecond)
	
	// 停止
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	
	// 应该在 1 秒内停止
	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("Stop() did not complete within 1 second")
	}
}

func TestVacuumWorker_CheckAndExecute_NotExecuteHour(t *testing.T) {
	w := NewVacuumWorker(nil)
	
	// 设置执行时间为当前时间 + 1 小时（确保不会执行）
	w.SetExecuteHour((time.Now().Hour() + 1) % 24)
	w.SetInterval(1 * time.Minute) // 短间隔便于测试
	
	ctx := context.Background()
	
	// 记录初始状态
	before := w.lastExecuted
	
	// 调用检查
	w.checkAndExecute(ctx)
	
	// 不应该执行（因为不是执行时间）
	if !w.lastExecuted.Equal(before) {
		t.Error("vacuum should not execute when hour does not match")
	}
}

func TestVacuumWorker_CheckAndExecute_TooSoon(t *testing.T) {
	w := NewVacuumWorker(nil)
	
	// 设置为当前小时
	w.SetExecuteHour(time.Now().Hour())
	w.SetInterval(1 * time.Hour)
	
	// 模拟刚刚执行过
	w.lastExecuted = time.Now().Add(-30 * time.Minute)
	
	ctx := context.Background()
	before := w.lastExecuted
	
	// 调用检查
	w.checkAndExecute(ctx)
	
	// 不应该执行（因为间隔未到）
	if !w.lastExecuted.Equal(before) {
		t.Error("vacuum should not execute when interval has not passed")
	}
}
