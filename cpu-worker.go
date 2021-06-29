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

var traceFlag = true

// duration between (task repay p to scheduler, scheduler rcv this p )
var traceMaxPdelay = time.Duration(0)

const DefaultMaxTimeSlice = time.Microsecond * 1000
const MaxEITaskTimeslice = time.Microsecond * 100
const MaxNewTaskTimeslice = time.Microsecond * 200

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
	// checkpoint
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

type taskSchTiming struct {
	resumeCpuT      time.Time
	suspendedCpuT   time.Time
	enterEventCallT time.Time
	endEventCallT   time.Time
	// event intensive factor
	eIfactor float32
	// sum and eiCt
	sumCpuDuration       time.Duration
	sumEventCallDuration time.Duration
	eiCt                 uint64
}

var zeroT = time.Time{}

func (t *Task) calcEventIntensiveScore() float32 {
	assert(false)
	return 0
}

type Task struct {
	validFlag bool
	// true: event intensive
	// false: cpu intensive
	eventIntensiveFlag bool
	// the bigger, the priority in scheduler would be higher
	eventIntensiveScore float32
	timing              taskSchTiming

	stat int
	fp0  func()
	fp1  func(func())
	fp2  func(func(func()))
	h    TaskHandle
	// must > 0
	maxTimeSlice time.Duration
	// must > 0
	// keep const after initialized
	initMaxTimeSlice time.Duration
	p                *P
	w                *Workers
	// rcv p and resume runnable task
	pch chan *P
}

func (t *Task) assetValid() {
	assert(t.validFlag && t.w != nil)
	ct := 0
	if t.fp0 != nil {
		ct++
	}
	if t.fp1 != nil {
		ct++
	}
	if t.fp2 != nil {
		ct++
	}
	assert(ct == 1)
	assert(t.stat == STAT_NEW || t.stat == STAT_RUNNING ||
		t.stat == STAT_SUSPENDED || t.stat == STAT_END,
	)
}

func (t *Task) timingStart(tm time.Time) {
	t.timing.resumeCpuT = zeroT
	t.timing.suspendedCpuT = zeroT
	t.timing.enterEventCallT = zeroT
	t.timing.endEventCallT = zeroT
	t.timing.eIfactor = 0
	t.timing.resumeCpuT = tm
}

func (t *Task) timingCk(tm time.Time) {
	t.timing.suspendedCpuT = tm
}

func (t *Task) timingEnterEventCall(tm time.Time) {
	t.timing.suspendedCpuT = tm
	t.timing.enterEventCallT = tm
}

func (t *Task) timingEndEventCall(tm time.Time) {
	t.timing.endEventCallT = tm
}

func (t *Task) timingEnd(tm time.Time) {
	t.timing.suspendedCpuT = tm
}

/*
	new ① (
		(running ck ② runnable ① )
		|
		(running ③ eventCall ④ runnable ① )
		|
		(running ⑤ end)
	)*

	① timingStart
	② timingCk repayP calcEIfactorAndSumbitToRunnableTaskQueue
	③ timingEnterEventCall repayP
	④ timingEndEventCall calcEIfactorAndSumbitToRunnableTaskQueue
	⑤ timingEnd repayP
*/
func (t *Task) calcEIfactorAndSumbitToRunnableTaskQueue() {
	isCk := false
	tm := &t.timing
	push := func() {
		cpuDur := tm.suspendedCpuT.Sub(tm.resumeCpuT)
		eiDur := tm.endEventCallT.Sub(tm.enterEventCallT)
		assert(cpuDur >= 0 && eiDur >= 0)
		tm.sumCpuDuration += cpuDur
		tm.sumEventCallDuration += eiDur
		assert(cpuDur >= 0 && eiDur >= 0 && tm.sumCpuDuration >= 0 && tm.sumEventCallDuration >= 0)
	}
	calcEiFactor := func() float32 {
		if tm.suspendedCpuT.Sub(tm.resumeCpuT) >= time.Millisecond {
			return 0
		}
		eiDdiv10 := tm.sumEventCallDuration >> 3
		var ret float32
		if tm.sumCpuDuration < time.Microsecond*10 {
			return 1.0
		}
		if tm.sumCpuDuration < eiDdiv10 {
			if tm.sumCpuDuration <= 0 {
				ret = float32(tm.sumEventCallDuration) / float32(1)
			} else {
				ret = float32(tm.sumEventCallDuration) / float32(tm.sumCpuDuration)
			}
			if tm.sumCpuDuration > time.Second {
				tm.sumCpuDuration = 0
				tm.sumEventCallDuration = 0
			}
			return ret
		} else {
			return 0
		}
	}
	if tm.enterEventCallT == zeroT && tm.endEventCallT == zeroT {
		isCk = true
	}
	push()
	var eIfactor float32
	if isCk {
		if tm.eiCt > 0 {
			eIfactor = calcEiFactor()
		} else {
		}
	} else {
		eIfactor = calcEiFactor()
	}

	if eiFactorBt0(eIfactor) {
		tm.eiCt += 1
		assert(tm.eiCt > 0)
		tm.eIfactor = eIfactor
		t.maxTimeSlice = t.initMaxTimeSlice
		t.w.runnableEventIntensiveTaskCh <- t
	} else {
		eIfactor = 0
		tm.eIfactor = eIfactor
		tm.eiCt = 0
		tm.sumCpuDuration = 0
		tm.sumEventCallDuration = 0
		t.maxTimeSlice = t.initMaxTimeSlice
		t.w.runnableCpuIntensiveTaskCh <- t
	}
}

// > 0
func eiFactorBt0(factor float32) bool {
	if factor > 0.0001 {
		return true
	} else {
		return false
	}
}

func (t *Task) calcMaxTimeSlice() time.Duration {
	var smallest time.Duration
	if t.maxTimeSlice < t.initMaxTimeSlice {
		smallest = t.maxTimeSlice
	} else {
		smallest = t.initMaxTimeSlice
	}
	if smallest < t.w.maxTimeSlice {
	} else {
		smallest = t.w.maxTimeSlice
	}
	assert(smallest > 0)
	return smallest
}

// start running a newTask or a suspended task
func (t *Task) resume(p *P, eiFlag bool, newFlag bool) {
	t.assetValid()
	p.assetValid()
	fixMaxTimeSlice := func() {
		if eiFlag {
			if t.initMaxTimeSlice > MaxEITaskTimeslice {
				if t.maxTimeSlice > MaxEITaskTimeslice {
					t.maxTimeSlice = MaxEITaskTimeslice
				}
			}
		}
		if newFlag {
			if t.initMaxTimeSlice > MaxNewTaskTimeslice {
				if t.maxTimeSlice > MaxNewTaskTimeslice {
					t.maxTimeSlice = MaxNewTaskTimeslice
				}
			}
		}
	}
	if t.stat == STAT_NEW {
		assert(t.p == nil)
		t.p = p
		finalFp := func() {
			fixMaxTimeSlice()
			t.timingStart(time.Now())
			if t.fp1 != nil {
				t.fp1(func() {
					checkPoint(t)
				})
			} else if t.fp0 != nil {
				t.fp0()
			} else {
				assert(t.fp2 != nil)
				t.fp2(func(ecfp func()) {
					if ecfp == nil {
						checkPoint(t)
					} else {
						eventRoutineCall(t, ecfp)
					}
				})
			}
			t.p.assetValid()
			nowT := time.Now()
			if traceFlag {
				t.p.taskRepayPt = nowT
			}
			t.w.repayP(t.p)
			t.timingEnd(nowT)
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
		fixMaxTimeSlice()
		tryMustSndPch(t.pch, p)
	}
}

func checkPoint(t *Task) {
	yieldFlag := atomic.LoadUint32(&t.h.yieldFlag)
	if yieldFlag != 0 {
		// should yield
		assert(t.p != nil && t.stat == STAT_RUNNING)
		p := t.p
		t.p = nil
		t.stat = STAT_SUSPENDED
		nowT := time.Now()
		t.timingCk(nowT)
		if traceFlag {
			p.taskRepayPt = nowT
		}
		atomic.StoreUint32(&t.h.yieldFlag, 0)
		tryMustSndPch(t.w.availablePchan, p)
		t.calcEIfactorAndSumbitToRunnableTaskQueue()
		// block at here untill scheduler wants us to resume
		var ok bool
		t.p, ok = <-t.pch
		assert(ok)
		t.p.assetValid()
		t.stat = STAT_RUNNING
		t.timingStart(time.Now())
	}
}

func eventRoutineCall(t *Task, eventRoutineFp func()) {
	nowT := time.Now()
	t.timingEnterEventCall(nowT)
	assert(t.p != nil && t.stat == STAT_RUNNING)
	p := t.p
	t.p = nil
	assert(p.eventCallTask == nil)
	p.eventCallTask = t
	t.stat = STAT_SUSPENDED
	if traceFlag {
		p.taskRepayPt = nowT
	}
	tryMustSndPch(t.w.availablePchan, p)
	{
		eventRoutineFp()
	}
	t.timingEndEventCall(time.Now())
	t.calcEIfactorAndSumbitToRunnableTaskQueue()
	// block at here until scheduler wants us to resume
	var ok bool
	t.p, ok = <-t.pch
	assert(ok)
	t.p.assetValid()
	t.stat = STAT_RUNNING
	t.timingStart(time.Now())
}

func (t *Task) sendSuspendSignal() {
	atomic.CompareAndSwapUint32(&t.h.yieldFlag, 0, 1)
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

func (w *Workers) repayP(p *P) {
	p.assetValid()
	tryMustSndPch(w.availablePchan, p)
}

type P struct {
	validFlag     bool
	idx           int
	eventCallTask *Task
	taskRepayPt   time.Time
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
	newTaskCh                    chan *Task
	runnableEventIntensiveTaskCh chan *Task
	runnableCpuIntensiveTaskCh   chan *Task
	availablePchan               chan *P
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
		maxTimeSlice := v.taskPtr.calcMaxTimeSlice()
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
		newTaskCh:                    make(chan *Task, 1024*p),
		runnableEventIntensiveTaskCh: make(chan *Task, 1024*p),
		runnableCpuIntensiveTaskCh:   make(chan *Task, 1024*p),
		availablePchan:               make(chan *P, p),
		maxTimeSlice:                 maxTimeSlice,
		taskSchArray:                 make([]taskSchUnit, p),
		exitCh:                       make(chan struct{}),
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
	// eiT local buf
	var eiTask *Task
	eiTaskPq := newPrioTaskQueue()
	// local cpuT buf
	var cpuTask *Task
	// local newT buf
	var newTask *Task
	// local p buf
	var newp *P
	var pArray []*P
	tryToPushAllEiT := func() {
		if eiTask != nil {
			eiTaskPq.Push(eiTask, eiTask.timing.eIfactor)
			eiTask = nil
		}
		for {
			var eiT *Task
			select {
			case eiT = <-w.runnableEventIntensiveTaskCh:
				eiT.assetValid()
			default:
			}
			if eiT != nil {
				eiTaskPq.Push(eiT, eiT.timing.eIfactor)
			} else {
				break
			}
		}
	}
	// return (task, eiFlag, newFlag)
	mustGetTnb := func() (*Task, bool, bool) {
		// priority:
		//   eIQ > newQ > cIQ
		tryToPushAllEiT()
		if eiTaskPq.Len() > 0 {
			return eiTaskPq.Pop().t, true, false
		}
		var ret *Task
		if newTask != nil {
			ret = newTask
			newTask = nil
			return ret, false, true
		}
		select {
		case ret = <-w.newTaskCh:
			return ret, false, true
		default:
		}
		if cpuTask != nil {
			ret = cpuTask
			cpuTask = nil
			return ret, false, false
		}
		// try to get one runnable task
		select {
		case ret = <-w.newTaskCh:
			return ret, false, true
		case ret = <-w.runnableCpuIntensiveTaskCh:
			return ret, false, false
		default:
		}
		panic("unexpected")
	}
	hasTask := func() bool {
		if eiTask != nil || newTask != nil || cpuTask != nil ||
			eiTaskPq.Len() > 0 || len(w.newTaskCh) > 0 ||
			len(w.runnableCpuIntensiveTaskCh) > 0 ||
			len(w.runnableEventIntensiveTaskCh) > 0 {
			return true
		} else {
			return false
		}
	}
	pushNewP := func(newp *P) {
		assert(newp != nil)
		newp.assetValid()
		if traceFlag && newp.taskRepayPt != zeroT {
			d := time.Now().Sub(newp.taskRepayPt)
			if d > traceMaxPdelay {
				traceMaxPdelay = d
			}
		}
		tu := w.taskSchArray[newp.idx]
		if tu.validFlag {
			tu.assertValid()
			w.taskSchArray[newp.idx] = taskSchUnit{}
		}
		if newp.eventCallTask != nil {
			t := newp.eventCallTask
			if tu.validFlag {
				assert(tu.taskPtr == t)
			}
			newp.eventCallTask = nil
		}
		pArray = append(pArray, newp)
		newp = nil
	}
	tryToPushAllP := func() {
		if newp != nil {
			pushNewP(newp)
			newp = nil
		}
		for {
			var p *P
			select {
			case p = <-w.availablePchan:
				p.assetValid()
			default:
			}
			if p != nil {
				pushNewP(p)
			} else {
				break
			}
		}
	}
	mustGetPnb := func() *P {
		tryToPushAllP()
		assert(len(pArray) > 0)
		p := pArray[len(pArray)-1]
		pArray = pArray[0 : len(pArray)-1]
		return p
	}
	hasP := func() bool {
		return newp != nil || len(pArray) > 0 || len(w.availablePchan) > 0
	}

	for {
		assert(newp == nil)
		tryToPushAllP()
		if hasP() {
			goto P_AVAILABLE
		}
		if hasTask() {
			goto NO_P_AND_HAS_RUNNABLE_TASK
		}
		goto NO_P_AND_NO_RUNNABLE_TASK
	NEW_P:
		{
			pushNewP(newp)
			newp = nil
			goto P_AVAILABLE
		}
	P_AVAILABLE:
		{
			assert(hasP())
			if hasTask() {
			} else {
				select {
				case newp = <-w.availablePchan:
					goto NEW_P
				case newTask = <-w.newTaskCh:
				case eiTask = <-w.runnableEventIntensiveTaskCh:
				case cpuTask = <-w.runnableCpuIntensiveTaskCh:
				}
				goto P_AVAILABLE_AND_HAS_RUNNABLE_TASK
			}
		}
	P_AVAILABLE_AND_HAS_RUNNABLE_TASK:
		{
			thisP := mustGetPnb()
			thisT, eiFlag, newFlag := mustGetTnb()
			w.taskSchArray[thisP.idx] = taskSchUnit{
				validFlag: true,
				resumeT:   time.Now(),
				taskPtr:   thisT,
			}
			thisT.resume(thisP, eiFlag, newFlag)
			goto GOTO_NEXT_LOOP
		}
	NO_P_AND_NO_RUNNABLE_TASK:
		{
			select {
			case newp = <-w.availablePchan:
				goto NEW_P
			case newTask = <-w.newTaskCh:
			case eiTask = <-w.runnableEventIntensiveTaskCh:
			case cpuTask = <-w.runnableCpuIntensiveTaskCh:
			}
			goto NO_P_AND_HAS_RUNNABLE_TASK
		}
	NO_P_AND_HAS_RUNNABLE_TASK:
		{
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
				tu.assertValid()
				w.taskSchArray[idx] = taskSchUnit{}
				tu.taskPtr.sendSuspendSignal()
				if timer != nil {
					timer.Stop()
				}
				goto GOTO_NEXT_LOOP
			case newp = <-w.availablePchan:
				if timer != nil {
					timer.Stop()
				}
				goto NEW_P
			}
		}
	GOTO_NEXT_LOOP:
		assert(newp == nil)
		continue
	}
}

func (w *Workers) GetMaxP() int {
	return cap(w.availablePchan)
}

func (w *Workers) Submit(fp0 func()) *TaskHandle {
	return w.submit(fp0, nil, nil, DefaultMaxTimeSlice, false)
}

func (w *Workers) Submit1(fp1 func(func())) *TaskHandle {
	return w.submit(nil, fp1, nil, DefaultMaxTimeSlice, false)
}

func (w *Workers) Submit2(fp1 func(func()), maxTimeSlice time.Duration) *TaskHandle {
	return w.submit(nil, fp1, nil, maxTimeSlice, false)
}

func (w *Workers) Submit3(fp2 func(func(func())), maxTimeSlice time.Duration, eiFlag bool) *TaskHandle {
	return w.submit(nil, nil, fp2, maxTimeSlice, eiFlag)
}

func (w *Workers) SubmitX(fp0 func(), fp1 func(func()), fp2 func(func(func())), maxTimeSlice time.Duration, eiFlag bool) *TaskHandle {
	return w.submit(fp0, fp1, fp2, maxTimeSlice, eiFlag)
}

func (w *Workers) submit(fp0 func(), fp1 func(func()), fp2 func(func(func())), maxTimeSlice time.Duration, eiFlag bool) *TaskHandle {
	if maxTimeSlice <= 0 {
		maxTimeSlice = DefaultMaxTimeSlice
	}
	task := Task{
		validFlag: true,
		timing:    taskSchTiming{},
		stat:      STAT_NEW,
		fp0:       fp0,
		fp1:       fp1,
		fp2:       fp2,
		h: TaskHandle{
			done:     make(chan struct{}),
			yieldCh:  make(chan *P, 1),
			resumeCh: make(chan *P, 1),
		},
		maxTimeSlice:     maxTimeSlice,
		initMaxTimeSlice: maxTimeSlice,
		w:                w,
		pch:              make(chan *P, 1),
	}
	if eiFlag {
		w.runnableEventIntensiveTaskCh <- &task
	} else {
		w.newTaskCh <- &task
	}
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
	return GetGlobalWorkers().submit(fp0, nil, nil, DefaultMaxTimeSlice, false)
}

func Submit1(fp1 func(func())) *TaskHandle {
	return GetGlobalWorkers().submit(nil, fp1, nil, DefaultMaxTimeSlice, false)
}

func Submit2(fp1 func(func()), maxTimeSlice time.Duration) *TaskHandle {
	return GetGlobalWorkers().submit(nil, fp1, nil, maxTimeSlice, false)
}

func Submit3(fp2 func(func(func())), maxTimeSlice time.Duration, eiFlag bool) *TaskHandle {
	return GetGlobalWorkers().submit(nil, nil, fp2, maxTimeSlice, eiFlag)
}

func SubmitX(fp0 func(), fp1 func(func()), fp2 func(func(func())), maxTimeSlice time.Duration, eiFlag bool) *TaskHandle {
	return GetGlobalWorkers().submit(fp0, fp1, fp2, maxTimeSlice, eiFlag)
}

/*
func (w *Workers) Destroy() {
	close(w.exitCh)
}
*/

func GetTraceMaxPdelay() time.Duration {
	return traceMaxPdelay
}

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
