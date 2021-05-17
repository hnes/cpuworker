# cpuworker

## Status

Working in process.

## Demo

Make sure the GOMAXPROCS is bigger than 1 and there is at least `GOMAXPROCS` physical OS threads available.

Run the example/demo.go.

```bash
# cmd 1
while true; do sleep 1;ab -s10000000 -c 10 -n 60 http://127.0.0.1:8080/delay1ms; done

# cmd 2
while true; do sleep 1;ab -s10000000 -c 10 -n 60 http://127.0.0.1:8080/checksumWithoutCpuWorker; done

# cmd 3
while true; do sleep 1;ab -s10000000 -c 10 -n 60 http://127.0.0.1:8080/checksumWithCpuWorker; done
```

Step 1: Killall already existing cmd `x`, then run the cmd 1.

Step 2: Killall already existing cmd `x`, then run the cmd 1 and cmd 2 simultaneously.

Step 3: Killall already existing cmd `x`, then run the cmd 1 and cmd 3 simultaneously.

Please watch the latency which cmd 1 yields carefully at every step and then you would catch the difference :-D

## Contributing

Welcome to contribute 🎉🎉🎉

Before your pull request is merged, you must sign the [Developer Certificate of Origin](https://developercertificate.org/). Please visit the [DCO](https://github.com/apps/dco) for more information. Basically you add the `Signed-off-by: Real Name <e@mail.addr>` inside the commit message to claim all your contribution adheres the requirements inside the [DCO](https://github.com/apps/dco).

## Copyright and License

Copyright (C) 2021, by the cpuworker Authors

Unless otherwise noted, the cpuworker source files are distributed under the Apache License Version 2.0. See the [LICENSE](LICENSE) file for details.
