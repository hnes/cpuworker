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

type prioTaskHeapUnit struct {
	score float32
	seq   uint64
	t     *Task
}

// cmpPrio
// 1: u1 > u2
// 0: u1 == u2
// -1: u1 < u2
func cmpPrio(u1, u2 *prioTaskHeapUnit) int {
	if u1.score > u2.score {
		return 1
	} else {
		if u1.score < u2.score {
			return -1
		} else {
			if u1.seq > u2.seq {
				return 1
			} else {
				if u1.seq < u2.seq {
					return -1
				} else {
					return 0
				}
			}
		}
	}
}

type prioTaskHeap []prioTaskHeapUnit

func (h prioTaskHeap) Len() int { return len(h) }

func (h prioTaskHeap) Less(i, j int) bool {
	if cmpPrio(&h[i], &h[j]) == 1 {
		return true
	} else {
		return false
	}
}

func (h prioTaskHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *prioTaskHeap) Push(x interface{}) {
	// Push and Pop use pointer receivers because they modify the slice's length,
	// not just its contents.
	*h = append(*h, x.(prioTaskHeapUnit))
}

func (h *prioTaskHeap) PeekTopest() prioTaskHeapUnit {
	assert(h.Len() > 0)
	return (*h)[0]
}

func (h *prioTaskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type prioTaskQueue struct {
	// max allocated seq
	seq uint64
	h   prioTaskHeap
}

func newPrioTaskQueue() *prioTaskQueue {
	return &prioTaskQueue{
		h: make(prioTaskHeap, 0, 128),
	}
}

func (pq *prioTaskQueue) Len() int {
	return pq.h.Len()
}

func (pq *prioTaskQueue) Pop() prioTaskHeapUnit {
	assert(pq.Len() > 0)
	pu, ok := pq.h.Pop().(prioTaskHeapUnit)
	assert(ok)
	return pu
}

func (pq *prioTaskQueue) PeekTopest() prioTaskHeapUnit {
	assert(pq.Len() > 0)
	return pq.h.PeekTopest()
}

func (pq *prioTaskQueue) Push(t *Task, score float32) {
	seq := pq.seq + 1
	pq.seq = seq
	assert(seq != 0 && t != nil && score >= 0)
	pu := prioTaskHeapUnit{
		score: score,
		seq:   seq,
		t:     t,
	}
	pq.h.Push(pu)
	return
}
