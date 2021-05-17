// Copyright 2021 The cpuworker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cpuworker

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var glWorkers *Workers
var glWorkersLock sync.Mutex

const DefaultMaxTimeSlice = time.Millisecond * 10

func init() {
	cpuP := CalcAutoP()
	workers := NewWorkers(cpuP, DefaultMaxTimeSlice)
	SetGlobalWorkers(workers)
}

const (
	STAT_NEW = iota
	STAT_RUNNING
	STAT_SUSPENDED
	STAT_END
)

type TaskHandle struct {
	// closed indicating the task is ended
	done chan struct{}
	// non-zero means should yield
	yieldFlag uint32
	// send one struct to this chan to tell manager current cpu worker is entering yield state
	yieldCh chan *P
	// receive one struct from this chan indicating current cpu worker should to be resumed
	resumeCh chan *P
}

func (h *TaskHandle) Sync() {
	<-h.done
}

type Task struct {
	validFlag bool
	stat      int
	fp0       func()
	fp1       func(func())
	h         TaskHandle
	// must > 0
	maxTimeSlice time.Duration
	p            *P
	w            *Workers
	// to resume suspended task
	pch chan *P
}

func (t *Task) assetValid() {
	assert(t.validFlag && t.w != nil)
	assert((t.fp0 != nil && t.fp1 == nil) || (t.fp0 == nil && t.fp1 != nil))
	assert(t.stat == STAT_NEW || t.stat == STAT_RUNNING ||
		t.stat == STAT_SUSPENDED || t.stat == STAT_END,
	)
}

func checkPoint(t *Task) {
	yieldFlag := atomic.LoadUint32(&t.h.yieldFlag)
	if yieldFlag != 0 {
		// should yield
		assert(t.p != nil && t.stat == STAT_RUNNING)
		p := t.p
		t.p = nil
		t.stat = STAT_SUSPENDED
		tryMustSndPch(t.h.yieldCh, p)
		t.w.suspendedTaskCh <- t
		// block at here untill manager wants us to resume
		var ok bool
		t.p, ok = <-t.h.resumeCh
		assert(ok)
		t.p.assetValid()
		t.stat = STAT_RUNNING
	}
}

func (t *Task) sendSuspendSignal() {
	go func() {
		assert(atomic.CompareAndSwapUint32(&t.h.yieldFlag, 0, 1))
		var p *P
		var ok bool
		select {
		case <-t.h.done:
			// the task itself would repay P to workers when it meets the ending
			assert(atomic.CompareAndSwapUint32(&t.h.yieldFlag, 1, 0))
			goto END
		case p = <-t.h.yieldCh:
			t.w.repayP(p)
		}
		// reset the yieldFlag since the target had already handoff its P
		assert(atomic.CompareAndSwapUint32(&t.h.yieldFlag, 1, 0))
		// block and get P
		// (monitor will send a P to this chan when it decides to resume this suspended task)
		p, ok = <-t.pch
		assert(ok)
		p.assetValid()
		tryMustSndPch(t.h.resumeCh, p)
	END:
	}()
}

func tryMustRcvPch(pch chan *P) *P {
	select {
	case p := <-pch:
		return p
	default:
		assert(false)
	}
	assert(false)
	return nil
}

func tryMustSndPch(pch chan *P, p *P) {
	select {
	case pch <- p:
		return
	default:
		assert(false)
	}
	assert(false)
}

// start running a newTask or a suspended task
func (t *Task) resume(p *P) {
	t.assetValid()
	p.assetValid()
	if t.stat == STAT_NEW {
		assert(t.p == nil)
		t.p = p
		finalFp := func() {
			if t.fp1 != nil {
				t.fp1(func() {
					checkPoint(t)
				})
			} else {
				assert(t.fp0 != nil)
				t.fp0()
			}
			t.p.assetValid()
			t.w.repayP(t.p)
			t.p = nil
			t.stat = STAT_END
			close(t.h.done)
			assert(len(t.pch) == 0)
			close(t.pch)
		}
		t.stat = STAT_RUNNING
		go finalFp()
	} else {
		assert(t.stat == STAT_SUSPENDED && t.p == nil)
		tryMustSndPch(t.pch, p)
	}
}

func (w *Workers) repayP(p *P) {
	p.assetValid()
	tryMustSndPch(w.availablePchan, p)
}

type P struct {
	validFlag bool
	idx       int
}

func (p *P) assetValid() {
	assert(p.validFlag)
}

type taskSchUnit struct {
	validFlag bool
	resumeT   time.Time
	taskPtr   *Task
}

func (tu *taskSchUnit) assertValid() {
	assert(tu.validFlag && tu.taskPtr != nil)
}

type Workers struct {
	newTaskCh       chan *Task
	suspendedTaskCh chan *Task
	availablePchan  chan *P
	// must > 0
	maxTimeSlice time.Duration
	// idx is the idx of P, and member is taskSchUnit
	taskSchArray []taskSchUnit
	exitCh       chan struct{}
}

// if never timeout return (0, -1)
// if there is a already timeout taskSchUnit return (0, validIdx)
// otherwise normal return timeout > 0 and a valid idx
func (w *Workers) calcDurationToNextTimeSliceTimeout() (timeout time.Duration, idx int) {
	var smallestSuspendT time.Time
	validUnitCt := 0
	for i, v := range w.taskSchArray {
		if v.validFlag {
			validUnitCt++
			v.assertValid()
		} else {
			continue
		}
		maxTimeSlice := w.maxTimeSlice
		if v.taskPtr.maxTimeSlice < maxTimeSlice {
			maxTimeSlice = v.taskPtr.maxTimeSlice
		}
		assert(validUnitCt > 0)
		if validUnitCt == 1 {
			smallestSuspendT = v.resumeT.Add(maxTimeSlice)
			idx = i
		} else {
			thisSuspendT := v.resumeT.Add(maxTimeSlice)
			if thisSuspendT.Before(smallestSuspendT) {
				smallestSuspendT = thisSuspendT
				idx = i
			}
		}
	}
	if validUnitCt > 0 {
	} else {
		return 0, -1
	}
	nowT := time.Now()
	w.taskSchArray[idx].assertValid()
	if smallestSuspendT.After(nowT) {
		return smallestSuspendT.Sub(nowT), idx
	} else {
		return 0, idx
	}
}

func NewWorkers(p int, maxTimeSlice time.Duration) *Workers {
	assert(p > 0)
	w := Workers{
		newTaskCh:       make(chan *Task, 128*p),
		suspendedTaskCh: make(chan *Task, 128*p),
		availablePchan:  make(chan *P, p),
		maxTimeSlice:    maxTimeSlice,
		taskSchArray:    make([]taskSchUnit, p),
		exitCh:          make(chan struct{}),
	}
	for idx := range w.taskSchArray {
		w.availablePchan <- &P{
			validFlag: true,
			idx:       idx,
		}
	}
	go w.schedulerRoutine()
	return &w
}

func (w *Workers) schedulerRoutine() {
	closedCh := make(chan time.Time, 1)
	close(closedCh)
	var nilCh chan time.Time

	var p *P
	var task *Task
	for {
		assert(p == nil)
		select {
		case p = <-w.availablePchan:
			goto P_AVAILABLE
		default:
		}
		if task != nil {
			goto NO_P_AND_HAS_RUNNABLE_TASK
		} else {
			// try to get newTask
			select {
			case task = <-w.newTaskCh:
				goto NO_P_AND_HAS_RUNNABLE_TASK
			default:
			}
			// try to get one runnable task
			select {
			case task = <-w.newTaskCh:
			case task = <-w.suspendedTaskCh:
			default:
				goto NO_P_AND_NO_RUNNABLE_TASK
			}
			goto NO_P_AND_HAS_RUNNABLE_TASK
		}
	P_AVAILABLE:
		{
			p.assetValid()
			assert(p != nil)
			if task != nil {
			} else {
				// try get newTask
				select {
				case task = <-w.newTaskCh:
					goto P_AVAILABLE_AND_HAS_RUNNABLE_TASK
				default:
				}
				// get newTask failed
				// now block and get one runnable task
				select {
				case task = <-w.newTaskCh:
				case task = <-w.suspendedTaskCh:
				}
			}
		P_AVAILABLE_AND_HAS_RUNNABLE_TASK:
			assert(p != nil && task != nil)
			task.assetValid()
			w.taskSchArray[p.idx] = taskSchUnit{
				validFlag: true,
				resumeT:   time.Now(),
				taskPtr:   task,
			}
			task.resume(p)
			p = nil
			task = nil
			goto GOTO_NEXT_LOOP
		}
	NO_P_AND_NO_RUNNABLE_TASK:
		{
			assert(p == nil && task == nil)
			select {
			case task = <-w.newTaskCh:
				goto NO_P_AND_HAS_RUNNABLE_TASK
			case task = <-w.suspendedTaskCh:
				goto NO_P_AND_HAS_RUNNABLE_TASK
			case p = <-w.availablePchan:
				goto P_AVAILABLE
			}
		}
	NO_P_AND_HAS_RUNNABLE_TASK:
		{
			assert(p == nil && task != nil)
			task.assetValid()
			var timeoutCh <-chan time.Time
			timeout, idx := w.calcDurationToNextTimeSliceTimeout()
			var timer *time.Timer
			if idx < 0 {
				timeoutCh = nilCh
			} else { // validIdx
				assert(idx >= 0 && idx < len(w.taskSchArray))
				timeoutNs := int64(timeout)
				if timeoutNs <= 0 {
					timeoutCh = closedCh
				} else {
					timer = time.NewTimer(timeout)
					timeoutCh = timer.C
				}
			}
			select {
			case <-timeoutCh:
				tu := w.taskSchArray[idx]
				//fmt.Println(idx, timeout, tu)
				tu.assertValid()
				w.taskSchArray[idx] = taskSchUnit{}
				tu.taskPtr.sendSuspendSignal()
				if timer != nil {
					timer.Stop()
				}
				goto GOTO_NEXT_LOOP
			case p = <-w.availablePchan:
				if timer != nil {
					timer.Stop()
				}
				goto P_AVAILABLE
			}
		}
	GOTO_NEXT_LOOP:
		assert(p == nil)
		continue
		assert(false)
	}
}

func (w *Workers) GetMaxP() int {
	return cap(w.availablePchan)
}

func (w *Workers) Submit(fp0 func()) *TaskHandle {
	return w.submit(fp0, nil, DefaultMaxTimeSlice)
}

func (w *Workers) Submit1(fp1 func(func())) *TaskHandle {
	return w.submit(nil, fp1, DefaultMaxTimeSlice)
}

func (w *Workers) Submit2(fp1 func(func()), maxTimeSlice time.Duration) *TaskHandle {
	return w.submit(nil, fp1, maxTimeSlice)
}

func (w *Workers) submit(fp0 func(), fp1 func(func()), maxTimeSlice time.Duration) *TaskHandle {
	if maxTimeSlice <= 0 {
		maxTimeSlice = DefaultMaxTimeSlice
	}
	task := Task{
		validFlag: true,
		stat:      STAT_NEW,
		fp0:       fp0,
		fp1:       fp1,
		h: TaskHandle{
			done:     make(chan struct{}),
			yieldCh:  make(chan *P, 1),
			resumeCh: make(chan *P, 1),
		},
		maxTimeSlice: DefaultMaxTimeSlice,
		w:            w,
		pch:          make(chan *P, 1),
	}
	w.newTaskCh <- &task
	return &task.h
}

func SetGlobalWorkers(w *Workers) {
	glWorkersLock.Lock()
	glWorkers = w
	glWorkersLock.Unlock()
}

func GetGlobalWorkers() (w *Workers) {
	glWorkersLock.Lock()
	w = glWorkers
	glWorkersLock.Unlock()
	return
}

func Submit(fp0 func()) *TaskHandle {
	return GetGlobalWorkers().submit(fp0, nil, DefaultMaxTimeSlice)
}

func Submit1(fp1 func(func())) *TaskHandle {
	return GetGlobalWorkers().submit(nil, fp1, DefaultMaxTimeSlice)
}

func Submit2(fp1 func(func()), maxTimeSlice time.Duration) *TaskHandle {
	return GetGlobalWorkers().submit(nil, fp1, maxTimeSlice)
}

/*
func (w *Workers) Destroy() {
	close(w.exitCh)
}
*/

func CalcAutoP() int {
	nCPU := runtime.GOMAXPROCS(0)
	if nCPU <= 2 {
		return 1
	}
	if nCPU <= 5 {
		return nCPU - 1
	}
	if nCPU <= 7 {
		return nCPU - 2
	}
	return nCPU - (nCPU / 4)
}

func assert(b bool) {
	if b {
	} else {
		panic("unexpected")
	}
}
