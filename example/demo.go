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

package main

import (
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"log"
	_ "net/http/pprof"

	mathrand "math/rand"
	"net/http"
	"runtime"
	"time"

	"github.com/hnes/cpuworker"
)

var glCrc32bs = make([]byte, 1024*256)

func cpuIntensiveTask(amt int) uint32 {
	//ts := time.Now()
	var ck uint32
	for range make([]struct{}, amt) {
		ck = crc32.ChecksumIEEE(glCrc32bs)
	}
	//fmt.Println("log: crc32.ChecksumIEEE time cost (without checkpoint):", time.Now().Sub(ts))
	return ck
}

func cpuIntensiveTaskWithCheckpoint(amt int, checkpointFp func()) uint32 {
	//ts := time.Now()
	var ck uint32
	for range make([]struct{}, amt) {
		ck = crc32.ChecksumIEEE(glCrc32bs)
		checkpointFp()
	}
	//fmt.Println("log: crc32.ChecksumIEEE time cost (with checkpoint):", time.Now().Sub(ts))
	return ck
}

func handleChecksumWithoutCpuWorker(w http.ResponseWriter, _ *http.Request) {
	ts := time.Now()
	ck := cpuIntensiveTask(10000 + mathrand.Intn(10000))
	w.Write([]byte(fmt.Sprintln("crc32 (without cpuworker):", ck, "time cost:", time.Now().Sub(ts))))
}

func handleChecksumWithCpuWorkerAndHasCheckpoint(w http.ResponseWriter, _ *http.Request) {
	ts := time.Now()
	var ck uint32
	cpuworker.Submit1(func(checkpointFp func()) {
		ck = cpuIntensiveTaskWithCheckpoint(10000+mathrand.Intn(10000), checkpointFp)
	}).Sync()
	w.Write([]byte(fmt.Sprintln("crc32 (with cpuworker and checkpoint):", ck, "time cost:", time.Now().Sub(ts))))
}

func handleChecksumSmallTaskWithCpuWorker(w http.ResponseWriter, _ *http.Request) {
	ts := time.Now()
	var ck uint32
	cpuworker.Submit(func() {
		ck = cpuIntensiveTask(10)
	}).Sync()
	w.Write([]byte(fmt.Sprintln("crc32 (with cpuworker and small task):", ck, "time cost:", time.Now().Sub(ts))))
}

func handleDelay(w http.ResponseWriter, _ *http.Request) {
	t0 := time.Now()
	wCh := make(chan struct{})
	go func() {
		time.Sleep(time.Millisecond)
		wCh <- struct{}{}
	}()
	<-wCh
	w.Write([]byte(fmt.Sprintf("delayed 1ms, time cost %s :)\n", time.Now().Sub(t0))))
}

func handleDelayLoop(w http.ResponseWriter, _ *http.Request) {
	t0 := time.Now()
	for idx := range make([]byte, 10) {
		t0 := time.Now()
		wCh := make(chan struct{})
		go func() {
			time.Sleep(time.Millisecond)
			wCh <- struct{}{}
		}()
		<-wCh
		w.Write([]byte(fmt.Sprintf("delayed 1ms loop, idx:%d , time cost %s :)\n", idx, time.Now().Sub(t0))))
	}
	w.Write([]byte(fmt.Sprintf("delayed 1ms loop, final , total time cost %s :)\n", time.Now().Sub(t0))))
}

func handleDelayLoopWithCpuWorker(w http.ResponseWriter, _ *http.Request) {
	cpuworker.Submit3(func(eventCall func(func())) {
		t0 := time.Now()
		for idx := range make([]byte, 10) {
			t0 := time.Now()
			wCh := make(chan struct{})
			go func() {
				time.Sleep(time.Millisecond)
				wCh <- struct{}{}
			}()
			eventCall(func() {
				<-wCh
				w.Write([]byte(fmt.Sprintf("delayed 1ms loop with cpuworker, idx:%d , time cost %s :)\n", idx, time.Now().Sub(t0))))
			})
		}
		eventCall(func() {
			w.Write([]byte(fmt.Sprintf("delayed 1ms loop with cpuworker, final , total time cost %s :)\n", time.Now().Sub(t0))))
		})
	}, 0, true).Sync()
}

func main() {
	rand.Read(glCrc32bs)
	nCPU := runtime.GOMAXPROCS(0)
	cpuP := cpuworker.GetGlobalWorkers().GetMaxP()
	fmt.Println("GOMAXPROCS:", nCPU, "DefaultMaxTimeSlice:", cpuworker.DefaultMaxTimeSlice,
		"cpuWorkerMaxP:", cpuP, "length of crc32 bs:", len(glCrc32bs))
	http.HandleFunc("/checksumWithCpuWorker", handleChecksumWithCpuWorkerAndHasCheckpoint)
	http.HandleFunc("/checksumSmallTaskWithCpuWorker", handleChecksumSmallTaskWithCpuWorker)
	http.HandleFunc("/checksumWithoutCpuWorker", handleChecksumWithoutCpuWorker)
	http.HandleFunc("/delay1ms", handleDelay)
	http.HandleFunc("/delay1msLoop", handleDelayLoop)
	http.HandleFunc("/delay1msLoopWithCpuWorker", handleDelayLoopWithCpuWorker)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
