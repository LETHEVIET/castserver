<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { Button } from 'flowbite-svelte';

  let pc: RTCPeerConnection | null = null;
  let wsSignaling: WebSocket | null = null;
  let streaming = false;
  let showOverlay = true;
  let fullscreen = false;
  let idleText = 'Connecting...';
  let errorState = false;

  // WebRTC Telemetry
  let statsInterval: ReturnType<typeof setInterval> | undefined;
  let decodeTime = 0;
  let decodeFps = 0;
  let decodeBitrate = 0;
  let jitterBuffer = 0;
  let droppedFrames = 0;
  let resolution = '';

  let lastBytesReceived = 0;
  let lastFramesDecoded = 0;
  let lastDecodeTime = 0;
  let lastTime = 0;

  let overlayTimer: ReturnType<typeof setTimeout>;
  let videoEl: HTMLVideoElement;
  let wakeLock: any = null;

  function showWithTimer() {
    showOverlay = true;
    clearTimeout(overlayTimer);
    overlayTimer = setTimeout(() => { showOverlay = false; }, 3000);
  }

  function toggleOverlay() {
    showOverlay = !showOverlay;
    if (showOverlay) {
      clearTimeout(overlayTimer);
      overlayTimer = setTimeout(() => { showOverlay = false; }, 3000);
    }
  }

  async function requestWakeLock() {
    if (!('wakeLock' in navigator)) return;
    try { wakeLock = await (navigator as any).wakeLock.request('screen'); } catch { }
  }

  function releaseWakeLock() {
    if (wakeLock) { wakeLock.release().then(() => { wakeLock = null; }).catch(() => { }); }
  }

  function isFullscreen() {
    return !!(document.fullscreenElement || (document as any).webkitFullscreenElement);
  }

  function toggleFullscreen() {
    if (isFullscreen()) {
      const exit = document.exitFullscreen || (document as any).webkitExitFullscreen;
      if (exit) try { exit.call(document); } catch { }
    } else {
      const wrap = document.getElementById('wrap');
      if (!wrap) return;
      const req = wrap.requestFullscreen || (wrap as any).webkitRequestFullscreen;
      if (req) try { req.call(wrap); } catch { }
    }
  }

  function syncFs() {
    fullscreen = isFullscreen();
    if (fullscreen) requestWakeLock();
    else releaseWakeLock();
  }

  async function connect() {
    if (pc) return;
    idleText = 'Connecting...';
    errorState = false;

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = proto + '//' + window.location.host + '/webrtc/subscribe';

    wsSignaling = new WebSocket(wsUrl);

    pc = new RTCPeerConnection({ iceServers: [] });

    let candidateQueue: RTCIceCandidateInit[] = [];

    pc.addTransceiver('video', { direction: 'recvonly' });

    pc.onicecandidate = (event) => {
      if (event.candidate && wsSignaling && wsSignaling.readyState === WebSocket.OPEN) {
        wsSignaling.send(JSON.stringify({
          type: 'candidate',
          candidate: event.candidate
        }));
      }
    };

    pc.ontrack = (event) => {
      if (videoEl && event.streams[0]) {
        videoEl.srcObject = event.streams[0];
        if (!streaming) {
          streaming = true;
          showWithTimer();
        }
      }
    };

    pc.onconnectionstatechange = () => {
      if (!pc) return;
      if (pc.connectionState === 'connected') {
        idleText = 'Streaming';
      } else if (pc.connectionState === 'failed') {
        disconnect();
        idleText = 'Connection lost';
        errorState = true;
        showOverlay = true;
      }
    };

    wsSignaling.onopen = async () => {
      try {
        const offer = await pc!.createOffer();
        await pc!.setLocalDescription(offer);
        wsSignaling!.send(JSON.stringify({
          type: 'offer',
          sdp: pc!.localDescription!.sdp
        }));
      } catch (err: any) {
        disconnect();
        idleText = 'Connection failed';
        errorState = true;
        showOverlay = true;
      }
    };

    wsSignaling.onmessage = async (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        if (msg.type === 'error') {
          disconnect();
          idleText = 'No active stream' + (msg.sdp ? ': ' + msg.sdp : '');
          errorState = true;
          showOverlay = true;
        } else if (msg.type === 'answer') {
          await pc!.setRemoteDescription(new RTCSessionDescription(msg));
          idleText = 'Connecting to stream...';

          // Process queued candidates
          for (const candidate of candidateQueue) {
            try {
              await pc!.addIceCandidate(new RTCIceCandidate(candidate));
            } catch (e) {
              console.warn('Error adding queued ICE candidate:', e);
            }
          }
          candidateQueue = [];

          // Start WebRTC Telemetry Loop
          lastBytesReceived = 0;
          lastFramesDecoded = 0;
          lastDecodeTime = 0;
          lastTime = 0;
          statsInterval = setInterval(updateStats, 1000);
        } else if (msg.type === 'candidate') {
          if (msg.candidate) {
            if (pc && pc.remoteDescription) {
              await pc.addIceCandidate(new RTCIceCandidate(msg.candidate));
            } else {
              candidateQueue.push(msg.candidate);
            }
          }
        }
      } catch (err: any) {
        console.error('Error handling subscriber signaling message:', err);
      }
    };

    wsSignaling.onclose = () => {
      console.log('Subscriber signaling WebSocket closed');
      if (streaming) {
        disconnect();
        idleText = 'Connection closed';
        errorState = true;
        showOverlay = true;
      }
    };

    wsSignaling.onerror = () => {
      disconnect();
      idleText = 'Signaling error';
      errorState = true;
      showOverlay = true;
    };
  }

  async function updateStats() {
    if (!pc) return;
    try {
      const stats = await pc.getStats();
      let activeInbound: any = null;

      stats.forEach(report => {
        if (report.type === 'inbound-rtp' && report.kind === 'video') {
          activeInbound = report;
        }
      });

      if (activeInbound) {
        const bytes = activeInbound.bytesReceived || 0;
        const frames = activeInbound.framesDecoded || 0;
        const duration = activeInbound.totalDecodeTime || 0;
        const jitterBufferDelay = activeInbound.jitterBufferDelay || 0;
        const jitterBufferEmitted = activeInbound.jitterBufferEmittedCount || 1;

        if (lastBytesReceived && lastTime) {
          const dt = (activeInbound.timestamp - lastTime) / 1000;
          if (dt > 0) {
            decodeBitrate = Math.round(((bytes - lastBytesReceived) * 8) / 1000 / dt);
            decodeFps = Math.round((frames - lastFramesDecoded) / dt);
            const dframes = frames - lastFramesDecoded;
            if (dframes > 0) {
              decodeTime = Math.round(((duration - lastDecodeTime) / dframes) * 1000 * 10) / 10;
            }
            if (jitterBufferEmitted) {
              jitterBuffer = Math.round((jitterBufferDelay / jitterBufferEmitted) * 1000);
            }
          }
        }

        lastBytesReceived = bytes;
        lastFramesDecoded = frames;
        lastDecodeTime = duration;
        lastTime = activeInbound.timestamp;

        droppedFrames = activeInbound.framesDropped || 0;
        resolution = `${activeInbound.frameWidth || 0}x${activeInbound.frameHeight || 0}`;
      }
    } catch (e) {
      console.error('Error getting stats:', e);
    }
  }

  function waitForIceGathering(pc: RTCPeerConnection): Promise<void> {
    return new Promise((resolve) => {
      if (pc.iceGatheringState === 'complete') {
        resolve();
        return;
      }
      const timer = setTimeout(() => {
        console.log('viewer ICE gathering timeout, proceeding');
        resolve();
      }, 3000);
      const listener = () => {
        if (pc.iceGatheringState === 'complete') {
          clearTimeout(timer);
          pc.removeEventListener('icegatheringstatechange', listener);
          resolve();
        }
      };
      pc.addEventListener('icegatheringstatechange', listener);
    });
  }

  function disconnect() {
    if (statsInterval) {
      clearInterval(statsInterval);
      statsInterval = undefined;
    }
    if (wsSignaling) {
      try { wsSignaling.close(); } catch { }
      wsSignaling = null;
    }
    if (pc) { try { pc.close(); } catch {} pc = null; }
    streaming = false;
    if (videoEl) videoEl.srcObject = null;
  }

  onMount(() => {
    document.addEventListener('fullscreenchange', syncFs);
    document.addEventListener('webkitfullscreenchange', syncFs);
    document.addEventListener('visibilitychange', async () => {
      if (document.visibilityState === 'visible' && isFullscreen()) await requestWakeLock();
    });
    connect();
  });

  onDestroy(() => {
    document.removeEventListener('fullscreenchange', syncFs);
    document.removeEventListener('webkitfullscreenchange', syncFs);
    disconnect();
    releaseWakeLock();
  });
</script>

<div id="wrap" class="fixed inset-0 flex items-center justify-center bg-black">
  <video
    bind:this={videoEl}
    autoplay playsinline muted
    class="max-h-full max-w-full"
    class:hidden={!streaming}
  ></video>

  <div class="absolute inset-0 flex flex-col items-center justify-center gap-4 bg-gray-950 z-10" class:hidden={streaming}>
    <div class="relative w-15 h-15 rounded-full bg-emerald-500/10 flex items-center justify-center">
      <div class="absolute inset-0 rounded-full border-2 border-emerald-500 animate-ping opacity-20"></div>
      <div class="w-3 h-3 rounded-full bg-emerald-500 shadow-[0_0_12px_#10b981]"></div>
    </div>
    <div class="text-xs text-gray-400 font-medium text-center max-w-64 leading-relaxed">{idleText}</div>
    {#if errorState}
      <Button on:click={connect}>Retry</Button>
    {/if}
  </div>

  <div
    class="absolute top-4 right-4 z-30 flex items-center gap-2 bg-black/60 backdrop-blur-xl border border-white/10 px-3 py-1.5 rounded-full text-[11px] font-medium tracking-wide text-gray-400 shadow-lg transition-opacity duration-300"
    class:hidden={!streaming || !showOverlay}
  >
    <span class="w-1.5 h-1.5 rounded-full bg-emerald-500 shadow-[0_0_8px_#10b981]"></span>
    WebRTC Live
  </div>

  {#if streaming && showOverlay}
  <div
    class="absolute top-4 left-4 z-30 flex flex-col gap-1.5 bg-black/70 backdrop-blur-xl border border-white/10 p-3.5 rounded-xl text-[11px] font-medium text-gray-400 shadow-2xl transition-all duration-300"
  >
    <div class="text-[10px] font-semibold text-gray-500 uppercase tracking-wider mb-1 pb-1 border-b border-white/5">
      Receiver Telemetry
    </div>
    <div class="flex justify-between gap-5">
      <span>Resolution:</span>
      <span class="font-mono text-gray-200 font-semibold">{resolution || '—'}</span>
    </div>
    <div class="flex justify-between gap-5">
      <span>Frame Rate:</span>
      <span class="font-mono text-indigo-400 font-semibold">{decodeFps} FPS</span>
    </div>
    <div class="flex justify-between gap-5">
      <span>Incoming Bitrate:</span>
      <span class="font-mono text-amber-400 font-semibold">{decodeBitrate} kbps</span>
    </div>
    <div class="flex justify-between gap-5">
      <span>Decode Latency:</span>
      <span class="font-mono text-violet-400 font-semibold">{decodeTime} ms</span>
    </div>
    <div class="flex justify-between gap-5">
      <span>Jitter Buffer:</span>
      <span class="font-mono text-emerald-400 font-semibold">{jitterBuffer} ms</span>
    </div>
    <div class="flex justify-between gap-5">
      <span>Dropped Frames:</span>
      <span class="font-mono text-rose-400 font-semibold">{droppedFrames}</span>
    </div>
  </div>
  {/if}

  <div
    class="absolute bottom-6 left-1/2 -translate-x-1/2 z-30 flex items-center gap-2 bg-black/60 backdrop-blur-xl border border-white/10 px-3 py-2 rounded-full shadow-2xl transition-all duration-400"
    class:opacity-0={!showOverlay} class:pointer-events-none={!showOverlay} class:translate-y-5={!showOverlay}
  >
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => e.stopPropagation()}>
      <Button color="red" size="xs" on:click={() => { disconnect(); idleText = 'Disconnected'; errorState = true; showOverlay = true; }}>Disconnect</Button>
    </span>
    <span class="w-px h-5 bg-white/10"></span>
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => { e.stopPropagation(); toggleFullscreen(); showWithTimer(); }}>
      <Button color="dark" size="xs" on:click={toggleFullscreen}>{fullscreen ? 'Exit Fullscreen' : 'Fullscreen'}</Button>
    </span>
  </div>

  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="absolute inset-0 z-5" on:click={toggleOverlay}></div>
</div>
