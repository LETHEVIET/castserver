<script lang="ts">
  import { onMount, onDestroy } from 'svelte';

  let pc: RTCPeerConnection | null = null;
  let wsSignaling: WebSocket | null = null;
  let streaming = false;
  let isDark = true;

  function toggleTheme() {
    isDark = !isDark;
    if (isDark) {
      document.documentElement.classList.add('dark');
      localStorage.setItem('theme', 'dark');
    } else {
      document.documentElement.classList.remove('dark');
      localStorage.setItem('theme', 'light');
    }
  }

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

  // Reconnection and Server Discovery states
  let userDisconnected = false;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

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

  function scheduleReconnect() {
    if (userDisconnected) return;
    if (reconnectTimer) return;
    
    idleText = 'Searching for broadcast...';
    reconnectTimer = setTimeout(async () => {
      reconnectTimer = undefined;
      await attemptReconnect();
    }, 2000);
  }

  async function attemptReconnect() {
    if (userDisconnected) return;
    if (pc || wsSignaling) return;

    try {
      const resp = await fetch('/stats');
      if (resp.ok) {
        const s = await resp.json();
        if (s.session_active) {
          connect();
        } else {
          scheduleReconnect();
        }
      } else {
        scheduleReconnect();
      }
    } catch {
      scheduleReconnect();
    }
  }

  async function connect() {
    if (pc) return;
    
    userDisconnected = false;
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
    }
    
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

  function disconnect(manual = false) {
    if (manual) {
      userDisconnected = true;
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = undefined;
      }
    }

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

    if (!userDisconnected) {
      scheduleReconnect();
    }
  }
  onMount(() => {
    isDark = document.documentElement.classList.contains('dark');
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
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
    }
    disconnect(true);
    releaseWakeLock();
  });</script>

<div id="wrap" class="fixed inset-0 flex items-center justify-center bg-zinc-100 dark:bg-[#050506] font-sans select-none transition-minimal" class:cursor-none={!showOverlay && fullscreen}>
  
  <!-- Main Stream Output Video -->
  <video
    bind:this={videoEl}
    autoplay playsinline muted
    class="w-full h-full z-0 object-contain transition-minimal"
    class:hidden={!streaming}
  ></video>

  <!-- Ultra-Minimalist Empty State Interface -->
  <div class="absolute inset-0 flex flex-col items-center justify-center gap-6 bg-zinc-50 dark:bg-[#050506] z-10 transition-colors duration-150" class:hidden={streaming}>
    <div class="relative w-12 h-12 flex items-center justify-center">
      <!-- Minimal thin loader -->
      <div class="w-8 h-8 rounded-full border border-zinc-200 dark:border-zinc-900 border-t-zinc-800 dark:border-t-zinc-300 animate-spin"></div>
    </div>
    
    <div class="text-xs text-zinc-500 dark:text-zinc-400 font-mono tracking-tight text-center max-w-xs leading-relaxed">
      {idleText}
    </div>
    
    {#if errorState}
      <button
        on:click={connect}
        class="mt-2 bg-zinc-950 hover:bg-zinc-800 text-white dark:bg-white dark:hover:bg-zinc-200 dark:text-black active-press text-[11px] font-mono font-medium py-2 px-5 rounded transition-minimal outline-none"
      >
        Retry Connection
      </button>
    {/if}
  </div>

  <!-- Live WebRTC Corner Indicator -->
  <div
    class="absolute top-6 right-6 z-30 flex items-center gap-2 bg-white/80 dark:bg-[#09090b]/80 backdrop-blur-md border border-zinc-200 dark:border-zinc-900 px-3 py-1.5 rounded text-[10px] font-mono text-zinc-600 dark:text-zinc-400 shadow-lg transition-minimal pointer-events-none"
    class:opacity-0={!streaming || !showOverlay}
    class:translate-y-[-4px]={!streaming || !showOverlay}
  >
    <span class="relative flex h-1.5 w-1.5">
      <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
      <span class="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500 status-ring-active"></span>
    </span>
    LIVE
  </div>

  <!-- Minimal Corner Telemetry Pane -->
  {#if streaming && showOverlay}
    <div
      class="absolute top-6 left-6 z-30 flex flex-col gap-2.5 bg-white/80 dark:bg-[#09090b]/80 backdrop-blur-md border border-zinc-200 dark:border-zinc-900 p-4 rounded text-[10px] text-zinc-600 dark:text-zinc-400 shadow-lg min-w-[200px] transition-minimal pointer-events-none fade-in-up font-mono"
    >
      <div class="text-[9px] font-semibold text-zinc-450 dark:text-zinc-500 uppercase tracking-wider mb-0.5 pb-1.5 border-b border-zinc-200 dark:border-zinc-900">
        Telemetry Data
      </div>
      <div class="flex justify-between gap-6">
        <span class="text-zinc-450 dark:text-zinc-500">Resolution</span>
        <span class="text-zinc-800 dark:text-zinc-300 font-medium tabular-nums">{resolution || '—'}</span>
      </div>
      <div class="flex justify-between gap-6">
        <span class="text-zinc-450 dark:text-zinc-500">Frame Rate</span>
        <span class="text-zinc-800 dark:text-zinc-300 font-medium tabular-nums">{decodeFps} FPS</span>
      </div>
      <div class="flex justify-between gap-6">
        <span class="text-zinc-450 dark:text-zinc-500">Bitrate</span>
        <span class="text-zinc-800 dark:text-zinc-300 font-medium tabular-nums">{decodeBitrate} kbps</span>
      </div>
      <div class="flex justify-between gap-6">
        <span class="text-zinc-450 dark:text-zinc-500">Latency</span>
        <span class="text-zinc-800 dark:text-zinc-300 font-medium tabular-nums">{decodeTime} ms</span>
      </div>
      <div class="flex justify-between gap-6">
        <span class="text-zinc-450 dark:text-zinc-500">Jitter Buffer</span>
        <span class="text-zinc-800 dark:text-zinc-300 font-medium tabular-nums">{jitterBuffer} ms</span>
      </div>
      <div class="flex justify-between gap-6">
        <span class="text-zinc-450 dark:text-zinc-500">Dropped Frames</span>
        <span class="text-rose-600 dark:text-rose-500 font-medium tabular-nums">{droppedFrames}</span>
      </div>
    </div>
  {/if}

  <!-- Beautiful Bottom Overlay Menu Bar -->
  <div
    class="absolute bottom-8 left-1/2 -translate-x-1/2 z-30 flex items-center gap-3 bg-white/95 dark:bg-[#09090b]/90 backdrop-blur-md border border-zinc-200 dark:border-zinc-900 px-4 py-2 rounded shadow-2xl transition-minimal"
    class:opacity-0={!showOverlay}
    class:pointer-events-none={!showOverlay}
    class:translate-y-2={!showOverlay}
  >
    <!-- Disconnect Trigger -->
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => e.stopPropagation()}>
      <button
        on:click={() => { disconnect(true); idleText = 'Disconnected'; errorState = true; showOverlay = true; }}
        class="bg-transparent hover:bg-rose-50/50 dark:hover:bg-rose-950/20 border border-zinc-250 dark:border-zinc-850 hover:border-rose-200 dark:hover:border-rose-900/50 text-rose-600 dark:text-rose-500 text-[10px] font-mono font-medium py-1.5 px-3.5 rounded transition-minimal outline-none"
      >
        Disconnect
      </button>
    </span>
    
    <span class="w-px h-3 bg-zinc-200 dark:bg-zinc-900"></span>

    <!-- Theme Toggle -->
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => { e.stopPropagation(); toggleTheme(); showWithTimer(); }}>
      <button
        on:click={toggleTheme}
        class="bg-zinc-50 hover:bg-zinc-100 dark:bg-zinc-900 dark:hover:bg-zinc-850 border border-zinc-200 dark:border-zinc-850 text-zinc-500 dark:text-zinc-400 hover:text-zinc-900 dark:hover:text-white text-[10px] font-mono font-medium p-1.5 rounded transition-minimal outline-none flex items-center justify-center"
        title="Toggle Theme"
      >
        {#if isDark}
          <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364-6.364l-.707.707M6.343 17.657l-.707.707m0-12.728l.707.707m12.728 12.728l.707-.707M12 8a4 4 0 100 8 4 4 0 000-8z"></path>
          </svg>
        {:else}
          <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"></path>
          </svg>
        {/if}
      </button>
    </span>

    <span class="w-px h-3 bg-zinc-200 dark:bg-zinc-900"></span>
    
    <!-- Fullscreen Toggle -->
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => { e.stopPropagation(); toggleFullscreen(); showWithTimer(); }}>
      <button
        on:click={toggleFullscreen}
        class="bg-zinc-50 hover:bg-zinc-100 dark:bg-zinc-900 dark:hover:bg-zinc-850 border border-zinc-200 dark:border-zinc-850 text-zinc-700 dark:text-zinc-300 text-[10px] font-mono font-medium py-1.5 px-3.5 rounded transition-minimal outline-none"
      >
        {fullscreen ? 'Exit Full' : 'Fullscreen'}
      </button>
    </span>
  </div>

  <!-- Transparent Fullscreen Interaction Mask to toggle controls overlay visibility -->
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="absolute inset-0 z-5" on:click={toggleOverlay}></div>
</div>
