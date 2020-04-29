function createButton(label, onclick) {
  const button = document.createElement('button')
  button.textContent = label
  button.onclick = onclick
  document.body.appendChild(button)
}

createButton('join', function() {
  document.body.innerHTML = ''

  const wsUrl = location.origin.replace(/^http/, 'ws') + '/ws/'
  const ws = new WebSocket(wsUrl)
  const pc = new RTCPeerConnection({
    iceServers: [{
			urls: ['stun:rtc.peercalls.com'],
    }],
  })

  let initialOffer = true
  const icePromise = new Promise(resolve => {
    if (!initialOffer) {
      resolve()
      return
    }
    pc.onicecandidate = e => {
      console.log('ice candidate', e.candidate)
    }
    pc.onicegatheringstatechange = e => {
      console.log('ice gathering state', e.target.iceGatheringState)
      if (e.target.iceGatheringState === 'complete') {
        initialOffer = false
        resolve()
      }
    }
  })

  const send = (type, payload) => ws.send(JSON.stringify({type, payload}))

  ws.addEventListener('open', () => {
    console.log('ws connected')
    setInterval(() => send('ping'), 5000)
    send('ready')
  })

  ws.addEventListener('message', async event => {
    const msg = JSON.parse(event.data)
    console.log('ws message', msg.type)
    switch (msg.type) {
      case 'offer':
        console.log(msg.payload.type, msg.payload.sdp)
        // const offer = await pc.createOffer()
        // pc.setLocalDescription(offer)
        // await icePromise
        // console.log(pc.localDescription.sdp)
        await pc.setRemoteDescription(msg.payload)
        const answer = await pc.createAnswer()
        console.log('setting local description')
        await pc.setLocalDescription(answer)
        console.log('awaiting ice gathering')
        await icePromise
        console.log('sending answer', pc.localDescription.sdp)
        send('answer', pc.localDescription)
    }
  })

  function addVideo(stream) {
    v = document.createElement('video')
    v.style.width = '200px'
    v.srcObject = stream
    document.body.appendChild(v)
    v.play()
  }

  function sendVideo(stream) {
    stream.getTracks().forEach(track => {
      pc.addTrack(track, stream)
    })
    send('pub')
  }

  pc.addEventListener('track', event => {
    event.streams.forEach(stream => {
      addVideo(stream)
    })
  })

  createButton('Add camera stream', async () => {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: false, video: true })
    addVideo(stream)
    sendVideo(stream)
  })

  createButton('Add desktop stream', async () => {
    const stream = await navigator.mediaDevices.getDisplayMedia({ audio: false, video: true })
    addVideo(stream)
    sendVideo(stream)
  })

  document.body.appendChild(document.createElement('div'))
})
