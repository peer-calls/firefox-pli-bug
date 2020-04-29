# Firefox PLI Bug

Firefox does not send a RTCP Picture Loss Indicator (PLI) after a second track
is added, and the second video never starts playing unless a PLI request is manually sent.

How to run:

```
go get github.com/peer-calls/firefox-pli-bug
firefox-pli
```

or

```
git clone https://github.com/peer-calls/firefox-pli-bug
cd firefox-pli-bug
go run main.go
```

Open two firefox windows and navigate to http://localhost:3000

1. Click `Join` in both windows and wait until connected
2. Click `Add Camera Stream` in window 1 and wait until video appears in window 2. Video should be playing normally.
3. Click `Add Desktop Stream` in window 1 and wait until grey box appears in window 2. Video does not play because PLI packet is not sent

Alternatively, run the application with `-pli-interval 3` CLI argument. The 2nd video will start playing normally.

Please shut down the server between tests because the cleanup is not implemented for this MVP.

The same test works in Chrome.

