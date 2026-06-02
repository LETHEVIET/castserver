<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { makeQR } from './lib/qrcode';
  import type { Preset, Stats } from './lib/api';

  let presets: Preset[] = [];
  let selectedPreset = '';
  let fps = '30';
  let casting = false;
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

  let ctrlMsg = '';
  let ctrlErr = false;

  let pc: RTCPeerConnection | null = null;
  let wsSignaling: WebSocket | null = null;
  let castStream: MediaStream | null = null;
  let online = false;
  let framesPublished: number | string = '—';
  let webSubscribers: number | string = '—';
  let sessionActive = false;

  // WebRTC Telemetry
  let statsInterval: ReturnType<typeof setInterval> | undefined;
  let encodeTime = 0;
  let encodeFps = 0;
  let encodeBitrate = 0;
  let rtt = 0;
  let qualityLimitation = 'none';
  let encoderInfo = '';

  let lastBytesSent = 0;
  let lastFramesEncoded = 0;
  let lastEncodeTime = 0;
  let lastTime = 0;

  let networkIps: string[] = [];
  let castPreview: HTMLVideoElement;

  let pollInterval: ReturnType<typeof setInterval> | undefined;

  $: presetItems = presets.map(p => ({ value: p.name, name: p.name }));
  const fpsItems = [
    { value: '10', name: '10' },
    { value: '15', name: '15' },
    { value: '20', name: '20' },
    { value: '30', name: '30' },
    { value: '60', name: '60' },
  ];

  const codecItems = [
    { value: 'auto', name: 'Auto (Browser Default)' },
    { value: 'h264', name: 'Force H.264 (NVENC / Software)' },
    { value: 'vp9', name: 'Force VP9 (Highly Optimized)' },
    { value: 'vp8', name: 'Force VP8 (Highly Optimized)' },
  ];
  let selectedCodec = 'auto';
  const modeItems = [
    { value: 'realtime', name: 'Realtime Mode' },
    { value: 'buffer', name: 'Buffer Mode' }
  ];
  let selectedMode = 'realtime';
  let shareAudio = false;

  // Svelte layout control states
  let copiedIp = '';
  let activeQrIp = '';

  function toggleQr(ip: string) {
    activeQrIp = activeQrIp === ip ? '' : ip;
  }

  function qrAction(node: HTMLElement, url: string) {
    node.appendChild(makeQR(url, 4));
    return {
      update(u: string) { node.innerHTML = ''; node.appendChild(makeQR(u, 4)); },
      destroy() { node.innerHTML = ''; },
    };
  }

  function setCtrl(msg: string, err: boolean) {
    ctrlMsg = msg;
    ctrlErr = err;
  }

  // Recycles browser media resources without telling the server to terminate subscribers
  function localCleanup() {
    if (statsInterval) {
      clearInterval(statsInterval);
      statsInterval = undefined;
    }
    if (wsSignaling) {
      try { wsSignaling.close(); } catch { }
      wsSignaling = null;
    }
    if (pc) {
      try { pc.close(); } catch { }
      pc = null;
    }
    if (castStream) {
      castStream.getTracks().forEach(t => t.stop());
      castStream = null;
    }
  }

  function castCleanup() {
    localCleanup();
    if (castPreview) {
      castPreview.style.display = 'none';
      castPreview.srcObject = null;
    }
    casting = false;
    fetch('/webrtc/stop', { method: 'POST' }).catch(() => {});
  }

  async function doStart() {
    if (!navigator.mediaDevices || !navigator.mediaDevices.getDisplayMedia) {
      setCtrl('getDisplayMedia not available in this browser.', true);
      return;
    }

    const p = presets.find(p => p.name === selectedPreset) || presets[0];
    const fpsVal = parseInt(fps, 10) || p?.fps || 30;

    if (casting) {
      setCtrl('Switching capture source...', false);
      localCleanup();
    } else {
      setCtrl('Requesting screen capture...', false);
    }

    try {
      const s = await navigator.mediaDevices.getDisplayMedia({
        video: {
          width: p.width > 0 ? { ideal: p.width } : undefined,
          height: p.height > 0 ? { ideal: p.height } : undefined,
          frameRate: { ideal: fpsVal },
          resizeMode: 'none',
        } as MediaTrackConstraints,
        audio: shareAudio,
      });
      castStream = s;
      if (!shareAudio) {
        s.getAudioTracks().forEach(t => s.removeTrack(t));
      }

      if (castPreview) {
        castPreview.srcObject = castStream;
        castPreview.style.display = 'block';
      }

      castStream.getVideoTracks()[0].onended = () => { doStop(); };

      setCtrl('Connecting signaling channel...', false);

      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = proto + '//' + window.location.host + '/webrtc/publish?mode=' + selectedMode;

      wsSignaling = new WebSocket(wsUrl);

      pc = new RTCPeerConnection({ iceServers: [] });

      let candidateQueue: RTCIceCandidateInit[] = [];

      pc.onicecandidate = (event) => {
        if (event.candidate && wsSignaling && wsSignaling.readyState === WebSocket.OPEN) {
          wsSignaling.send(JSON.stringify({
            type: 'candidate',
            candidate: event.candidate
          }));
        }
      };

      pc.onconnectionstatechange = () => {
        console.log('publisher conn state:', pc?.connectionState);
        if (pc && pc.connectionState === 'failed') {
          setCtrl('WebRTC connection lost.', true);
          castCleanup();
        }
      };

      const tracks = castStream!.getTracks();
      tracks.forEach(track => {
        if (track.kind === 'video' && 'contentHint' in track) {
          track.contentHint = selectedMode === 'realtime' ? 'motion' : 'detail';
        }
        const sender = pc!.addTrack(track, castStream!);

        // Apply selected codec preference
        if (track.kind === 'video' && selectedCodec !== 'auto' && typeof RTCRtpSender !== 'undefined' && 'getCapabilities' in RTCRtpSender) {
          const cap = RTCRtpSender.getCapabilities('video');
          if (cap && cap.codecs) {
            const preferredCodecs = cap.codecs.filter(c => c.mimeType.toLowerCase() === `video/${selectedCodec}`);
            const otherCodecs = cap.codecs.filter(c => c.mimeType.toLowerCase() !== `video/${selectedCodec}`);
            const transceiver = pc!.getTransceivers().find(t => t.sender === sender);
            if (transceiver && 'setCodecPreferences' in transceiver) {
              transceiver.setCodecPreferences([...preferredCodecs, ...otherCodecs]);
              console.log(`Preferred ${selectedCodec} codecs:`, preferredCodecs);
            }
          }
        }
      });

      wsSignaling.onopen = async () => {
        setCtrl('Sending offer...', false);
        try {
          const offer = await pc!.createOffer();
          await pc!.setLocalDescription(offer);

          wsSignaling!.send(JSON.stringify({
            type: 'offer',
            sdp: pc!.localDescription!.sdp
          }));
        } catch (err: any) {
          setCtrl('Offer creation failed: ' + err, true);
          castCleanup();
        }
      };

      wsSignaling.onmessage = async (ev) => {
        try {
          const msg = JSON.parse(ev.data);
          if (msg.type === 'answer') {
            setCtrl('Applying answer...', false);
            await pc!.setRemoteDescription(new RTCSessionDescription(msg));
            setCtrl('Casting ' + selectedPreset, false);
            casting = true;

            // Process queued candidates
            for (const candidate of candidateQueue) {
              try {
                await pc!.addIceCandidate(new RTCIceCandidate(candidate));
              } catch (e) {
                console.warn('Error adding queued ICE candidate:', e);
              }
            }
            candidateQueue = [];

            setTimeout(() => {
              const sender = pc?.getSenders().find(s => s.track?.kind === 'video');
              if (sender) {
                const params = sender.getParameters();
                if (!params.encodings) params.encodings = [{}];
                if (p.bitrate > 0) {
                  params.encodings[0].maxBitrate = p.bitrate * 1000;
                }
                params.degradationPreference = selectedMode === 'realtime' ? 'maintain-framerate' : 'maintain-resolution';
                sender.setParameters(params).catch(() => {});
                console.log('Applied encoder parameters: maxBitrate =', p.bitrate * 1000, 'degradationPreference =', params.degradationPreference);
              }
            }, 500);

            // Start WebRTC Telemetry Loop
            lastBytesSent = 0;
            lastFramesEncoded = 0;
            lastEncodeTime = 0;
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
          console.error('Error handling signaling message:', err);
        }
      };

      wsSignaling.onclose = () => {
        console.log('Signaling WebSocket closed');
        if (casting) {
          setCtrl('Cast connection closed.', false);
          castCleanup();
        }
      };

      wsSignaling.onerror = () => {
        setCtrl('Signaling error.', true);
        castCleanup();
      };
    } catch (err: any) {
      setCtrl('Cast failed: ' + (err.message || err), true);
      castCleanup();
    }
  }

  async function updateStats() {
    if (!pc) return;
    try {
      const stats = await pc.getStats();
      let activeOutbound: any = null;
      let remoteInbound: any = null;

      stats.forEach(report => {
        if (report.type === 'outbound-rtp' && report.kind === 'video') {
          activeOutbound = report;
        }
        if (report.type === 'remote-inbound-rtp' && report.kind === 'video') {
          remoteInbound = report;
        }
      });

      if (activeOutbound) {
        const bytes = activeOutbound.bytesSent || 0;
        const frames = activeOutbound.framesEncoded || 0;
        const duration = activeOutbound.totalEncodeTime || 0;

        if (lastBytesSent && lastTime) {
          const dt = (activeOutbound.timestamp - lastTime) / 1000;
          if (dt > 0) {
            encodeBitrate = Math.round(((bytes - lastBytesSent) * 8) / 1000 / dt);
            encodeFps = Math.round((frames - lastFramesEncoded) / dt);
            const dframes = frames - lastFramesEncoded;
            if (dframes > 0) {
              encodeTime = Math.round(((duration - lastEncodeTime) / dframes) * 1000 * 10) / 10;
            }
          }
        }

        lastBytesSent = bytes;
        lastFramesEncoded = frames;
        lastEncodeTime = duration;
        lastTime = activeOutbound.timestamp;

        qualityLimitation = activeOutbound.qualityLimitationReason || 'none';
        encoderInfo = activeOutbound.encoderImplementation || 'unknown';
      }

      if (remoteInbound) {
        rtt = Math.round((remoteInbound.roundTripTime || 0) * 1000);
      } else if (activeOutbound && activeOutbound.roundTripTime) {
        rtt = Math.round(activeOutbound.roundTripTime * 1000);
      }
    } catch (e) {
      console.error('Error getting stats:', e);
    }
  }

  function doStop() {
    setCtrl('Stopping...', false);
    castCleanup();
  }

  async function loadStats() {
    try {
      const s: Stats = await (await fetch('/stats')).json();
      framesPublished = s.frames_published ?? 0;
      webSubscribers = s.web_subscribers ?? 0;
      sessionActive = s.session_active;
    } catch { }
  }

  async function checkServer() {
    try {
      await fetch('/health');
      online = true;
    } catch {
      online = false;
    }
  }

  function viewerUrl(ip: string) {
    const proto = window.location.protocol;
    const port = window.location.port;
    return proto + '//' + ip + (port ? ':' + port : '') + '/web';
  }

  async function copyUrl(url: string, ip: string) {
    try {
      await navigator.clipboard.writeText(url);
      copiedIp = ip;
      setTimeout(() => { copiedIp = ''; }, 2000);
    } catch { }
  }

  async function loadPresets() {
    try {
      const list: Preset[] = await (await fetch('/presets')).json();
      presets = list;
      if (list.length > 0) {
        const nativePreset = list.find(p => p.name === 'Native');
        selectedPreset = nativePreset ? nativePreset.name : list[0].name;
      }
    } catch { }
  }

  async function loadInterfaces() {
    try {
      networkIps = await (await fetch('/interfaces')).json();
    } catch { }
  }

  onMount(() => {
    isDark = document.documentElement.classList.contains('dark');
    checkServer();
    loadPresets();
    loadStats();
    loadInterfaces();
    pollInterval = setInterval(() => {
      checkServer();
      loadStats();
    }, 2000);
  });
  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
    castCleanup();
  });
</script>

<div class="min-h-screen bg-white dark:bg-[#09090b] text-zinc-800 dark:text-[#e4e4e7] px-4 py-8 md:py-16 select-none font-sans fade-in-up transition-colors duration-150">
  <div class="mx-auto max-w-4xl">
    
    <!-- Ultra-Minimalist Header -->
    <header class="flex items-center justify-between pb-6 mb-10 border-b border-zinc-200 dark:border-zinc-900">
      <div class="flex items-center gap-3">
        <svg class="w-5 h-5 text-zinc-900 dark:text-zinc-100" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 10l4.553-2.276A1 1 0 0121 8.618v6.764a1 1 0 01-1.447.894L15 14M5 18h8a2 2 0 002-2V8a2 2 0 00-2-2H5a2 2 0 00-2 2v8a2 2 0 002 2z"></path>
        </svg>
        <span class="text-base font-semibold tracking-tight text-zinc-900 dark:text-white font-mono">castserver</span>
        <span class="inline-flex items-center rounded-md bg-zinc-100 dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800 px-2 py-0.5 text-[10px] font-medium text-zinc-600 dark:text-zinc-400 font-mono">
          v1.0 SFU
        </span>
      </div>
      <div class="flex items-center gap-3 shrink-0">
        <!-- Theme Toggle Button -->
        <button
          on:click={toggleTheme}
          class="flex items-center justify-center p-1.5 rounded-md border border-zinc-250 dark:border-zinc-800 bg-zinc-50 dark:bg-zinc-950 text-zinc-500 dark:text-zinc-400 hover:text-zinc-900 dark:hover:text-white transition-minimal outline-none"
          title="Toggle Theme"
        >
          {#if isDark}
            <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364-6.364l-.707.707M6.343 17.657l-.707.707m0-12.728l.707.707m12.728 12.728l.707-.707M12 8a4 4 0 100 8 4 4 0 000-8z"></path>
            </svg>
          {:else}
            <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"></path>
            </svg>
          {/if}
        </button>

        <div class="flex items-center gap-2 px-3 py-1 rounded-full bg-zinc-50 dark:bg-zinc-900/40 border border-zinc-200 dark:border-zinc-850">
          <span class="relative flex h-2 w-2">
            {#if online}
              <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
              <span class="relative inline-flex rounded-full h-2 w-2 bg-emerald-500 status-ring-active"></span>
            {:else}
              <span class="relative inline-flex rounded-full h-2 w-2 bg-rose-500"></span>
            {/if}
          </span>
          <span class="text-[11px] font-mono text-zinc-500 dark:text-zinc-400 uppercase tracking-wider">{online ? 'online' : 'offline'}</span>
        </div>
      </div>
    </header>

    <!-- Content Grid -->
    <div class="grid grid-cols-1 md:grid-cols-[1.1fr_0.9fr] gap-10">
      
      <!-- Left Column: Controls & Preview -->
      <div class="space-y-8">
        
        <!-- Stream Controller Card -->
        <div class="bg-zinc-50/50 dark:bg-zinc-900/20 border border-zinc-200 dark:border-zinc-900 rounded-lg p-6 transition-minimal">
          <div class="flex items-center justify-between pb-4 mb-5 border-b border-zinc-200 dark:border-zinc-900">
            <h2 class="text-xs font-semibold uppercase tracking-wider text-zinc-500 dark:text-zinc-400 font-mono">
              Broadcast Settings
            </h2>
            {#if casting}
              <span class="inline-flex items-center rounded bg-emerald-500/10 px-2 py-0.5 text-[10px] font-mono text-emerald-600 dark:text-emerald-400 border border-emerald-500/20">
                LIVE
              </span>
            {/if}
          </div>

          <div class="space-y-5">
            
            <!-- Double Grid: Preset & Mode -->
            <div class="grid grid-cols-2 gap-4">
              <div>
                <label for="preset-select" class="block text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider mb-2 font-mono">Preset Configuration</label>
                <div class="relative">
                  <select
                    id="preset-select"
                    bind:value={selectedPreset}
                    disabled={casting}
                    class="w-full select-minimal bg-zinc-50 hover:bg-zinc-100/50 dark:bg-zinc-950/60 dark:hover:bg-zinc-900/60 border border-zinc-250 dark:border-zinc-850 hover:border-zinc-350 dark:hover:border-zinc-800 disabled:opacity-50 text-zinc-800 dark:text-zinc-200 font-mono text-xs rounded-md px-3.5 py-2.5 outline-none transition-minimal focus:border-zinc-400 dark:focus:border-zinc-500 focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-500"
                  >
                    {#each presets as preset}
                      <option value={preset.name} class="bg-white dark:bg-zinc-950">{preset.name} ({preset.width > 0 ? `${preset.width}x${preset.height}` : 'Native'})</option>
                    {/each}
                  </select>
                </div>
              </div>

              <div>
                <label for="mode-select" class="block text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider mb-2 font-mono">Streaming Mode</label>
                <div class="relative">
                  <select
                    id="mode-select"
                    bind:value={selectedMode}
                    disabled={casting}
                    class="w-full select-minimal bg-zinc-50 hover:bg-zinc-100/50 dark:bg-zinc-950/60 dark:hover:bg-zinc-900/60 border border-zinc-250 dark:border-zinc-850 hover:border-zinc-350 dark:hover:border-zinc-800 disabled:opacity-50 text-zinc-800 dark:text-zinc-200 font-mono text-xs rounded-md px-3.5 py-2.5 outline-none transition-minimal focus:border-zinc-400 dark:focus:border-zinc-500 focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-500"
                  >
                    {#each modeItems as item}
                      <option value={item.value} class="bg-white dark:bg-zinc-950">{item.name}</option>
                    {/each}
                  </select>
                </div>
              </div>
            </div>

            <!-- Double Grid: FPS & Codec -->
            <div class="grid grid-cols-2 gap-4">
              <div>
                <label for="fps-select" class="block text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider mb-2 font-mono">Frame Rate</label>
                <div class="relative">
                  <select
                    id="fps-select"
                    bind:value={fps}
                    disabled={casting}
                    class="w-full select-minimal bg-zinc-50 hover:bg-zinc-100/50 dark:bg-zinc-950/60 dark:hover:bg-zinc-900/60 border border-zinc-250 dark:border-zinc-850 hover:border-zinc-350 dark:hover:border-zinc-800 disabled:opacity-50 text-zinc-800 dark:text-zinc-200 font-mono text-xs rounded-md px-3.5 py-2.5 outline-none transition-minimal focus:border-zinc-400 dark:focus:border-zinc-500 focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-500"
                  >
                    {#each fpsItems as item}
                      <option value={item.value} class="bg-white dark:bg-zinc-950">{item.name} FPS</option>
                    {/each}
                  </select>
                </div>
              </div>

              <div>
                <label for="codec-select" class="block text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider mb-2 font-mono">Video Codec</label>
                <div class="relative">
                  <select
                    id="codec-select"
                    bind:value={selectedCodec}
                    disabled={casting}
                    class="w-full select-minimal bg-zinc-50 hover:bg-zinc-100/50 dark:bg-zinc-950/60 dark:hover:bg-zinc-900/60 border border-zinc-250 dark:border-zinc-850 hover:border-zinc-350 dark:hover:border-zinc-800 disabled:opacity-50 text-zinc-800 dark:text-zinc-200 font-mono text-xs rounded-md px-3.5 py-2.5 outline-none transition-minimal focus:border-zinc-400 dark:focus:border-zinc-500 focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-500"
                  >
                    {#each codecItems as item}
                      <option value={item.value} class="bg-white dark:bg-zinc-950">{item.name}</option>
                    {/each}
                  </select>
                </div>
              </div>
            </div>

            <!-- Share System Audio Checkbox -->
            <div class="flex items-center gap-2 py-1">
              <input
                type="checkbox"
                id="share-audio"
                bind:checked={shareAudio}
                disabled={casting}
                class="w-3.5 h-3.5 rounded border-zinc-300 dark:border-zinc-800 bg-zinc-50 dark:bg-zinc-950 text-zinc-950 dark:text-white focus:ring-0 focus:ring-offset-0 outline-none transition-minimal cursor-pointer disabled:opacity-50"
              />
              <label for="share-audio" class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono cursor-pointer select-none disabled:opacity-50">
                Share System Audio (Requires browser support)
              </label>
            </div>

            <!-- Cast Action Buttons -->
            <div class="flex items-center gap-3 pt-3">
              <button
                on:click={doStart}
                class="flex-1 bg-zinc-950 hover:bg-zinc-800 text-white dark:bg-white dark:hover:bg-zinc-200 dark:text-black active-press transition-minimal font-medium text-xs py-2.5 px-4 rounded-md shadow-sm outline-none flex items-center justify-center gap-2"
              >
                <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 15l-2 5L9 9l11 4-5 2zm0 0l5 5M7.188 2.239l.777 2.897M5.136 7.965l-2.898-.777M13.95 4.05l-2.122 2.122m-5.657 5.656l-2.12 2.122"></path>
                </svg>
                {casting ? 'Switch Screen Source' : 'Share Screen'}
              </button>
              
              <button
                on:click={doStop}
                disabled={!casting}
                class="bg-transparent border border-zinc-200 hover:border-rose-200 dark:border-zinc-850 dark:hover:border-rose-900/50 hover:bg-rose-50/50 dark:hover:bg-rose-950/20 disabled:hover:bg-transparent disabled:border-zinc-150 dark:disabled:border-zinc-900 disabled:text-zinc-350 dark:disabled:text-zinc-650 disabled:cursor-not-allowed text-rose-600 dark:text-rose-500 active-press font-medium text-xs py-2.5 px-5 rounded-md transition-minimal outline-none"
              >
                Stop
              </button>
            </div>

            <!-- Feedback -->
            <div class="flex items-center gap-2 pt-2 border-t border-zinc-200 dark:border-zinc-900/50 text-[10px] font-mono">
              <span class="w-1.5 h-1.5 rounded-full {casting ? 'bg-emerald-500 status-ring-active' : ctrlErr ? 'bg-rose-500' : 'bg-zinc-400 dark:bg-zinc-600'}"></span>
              <p class="text-zinc-500 dark:text-zinc-400 truncate" class:text-rose-500={ctrlErr}>
                {ctrlMsg || (casting ? 'Broadcasting live stream output' : 'Ready to begin')}
              </p>
            </div>

          </div>
        </div>

        <!-- Video Stream Preview Pane -->
        <div class="relative overflow-hidden rounded-lg bg-black border border-zinc-200 dark:border-zinc-900 transition-minimal" style="display: {casting ? 'block' : 'none'}">
          <video
            bind:this={castPreview}
            autoplay muted playsinline
            class="w-full aspect-video rounded-lg bg-black"
          ></video>
          <div class="absolute top-3 left-3 bg-black/80 backdrop-blur-md border border-zinc-850 px-2 py-0.5 rounded text-[9px] font-mono tracking-wide text-zinc-400">
            LOCAL MONITOR FEED
          </div>
        </div>

      </div>

      <!-- Right Column: Share & HUD -->
      <div class="space-y-8">
        
        <!-- Distribution Share Hub -->
        <div class="bg-zinc-50/50 dark:bg-zinc-900/20 border border-zinc-200 dark:border-zinc-900 rounded-lg p-6 transition-minimal">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-zinc-500 dark:text-zinc-400 pb-4 mb-4 border-b border-zinc-200 dark:border-zinc-900 font-mono">
            Stream Distribution
          </h2>
          
          <div class="space-y-3">
            {#if networkIps.length === 0}
              <div class="flex flex-col items-center justify-center py-6 text-zinc-450 dark:text-zinc-550 gap-2">
                <svg class="w-5 h-5 animate-spin text-zinc-450 dark:text-zinc-500" fill="none" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
                  <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
                  <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
                </svg>
                <p class="text-[10px] font-mono">Detecting local network interfaces...</p>
              </div>
            {:else}
              {#each networkIps as ip}
                {@const url = viewerUrl(ip)}
                <div class="group flex flex-col gap-2 p-3 bg-zinc-50 dark:bg-zinc-950/40 border border-zinc-150 dark:border-zinc-900 hover:border-zinc-250 dark:hover:border-zinc-850 rounded-md transition-minimal">
                  <div class="flex items-center justify-between gap-3">
                    <span class="font-mono text-xs text-zinc-500 dark:text-zinc-400 group-hover:text-zinc-800 group-hover:dark:text-zinc-300 truncate tracking-tight">{url}</span>
                    <div class="flex items-center gap-1.5 shrink-0">
                      <!-- QR Toggle -->
                      <button
                        on:click={() => toggleQr(ip)}
                        class="text-[10px] font-mono font-medium text-zinc-500 dark:text-zinc-400 hover:text-zinc-900 dark:hover:text-white border border-zinc-200 dark:border-zinc-850 hover:border-zinc-300 dark:hover:border-zinc-700 px-2 py-1 rounded bg-zinc-100/50 dark:bg-zinc-900/20 hover:bg-zinc-100 dark:hover:bg-zinc-900/60 transition-minimal outline-none"
                      >
                        {activeQrIp === ip ? 'Hide' : 'QR'}
                      </button>
                      
                      <!-- Copy to Clipboard -->
                      <button
                        on:click={() => copyUrl(url, ip)}
                        class="text-[10px] font-mono font-medium transition-minimal px-2.5 py-1 rounded border min-w-16 text-center outline-none {copiedIp === ip ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-600 dark:text-emerald-400' : 'bg-zinc-100 dark:bg-zinc-900 border-zinc-200 dark:border-zinc-850 hover:bg-zinc-200 hover:dark:bg-zinc-850 hover:border-zinc-300 hover:dark:border-zinc-700 text-zinc-700 dark:text-zinc-200'}"
                      >
                        {copiedIp === ip ? 'Copied' : 'Copy'}
                      </button>
                    </div>
                  </div>

                  <!-- QR Code Slider Drawer -->
                  {#if activeQrIp === ip}
                    <div class="flex flex-col items-center gap-2 pt-3 border-t border-zinc-150 dark:border-zinc-900 mt-1 fade-in-up">
                      <div use:qrAction={url} class="bg-white p-2.5 rounded shadow-lg scale-[0.85] dark:filter dark:invert border border-zinc-200 dark:border-zinc-800"></div>
                      <p class="text-[9px] font-mono tracking-wider uppercase text-zinc-400 dark:text-zinc-500">Scan to watch live</p>
                    </div>
                  {/if}
                </div>
              {/each}
            {/if}
          </div>
        </div>

        <!-- Sleek Typographic Diagnostics HUD -->
        <div class="bg-zinc-50/50 dark:bg-zinc-900/20 border border-zinc-200 dark:border-zinc-900 rounded-lg p-6 transition-minimal">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-zinc-500 dark:text-zinc-400 pb-4 mb-5 border-b border-zinc-200 dark:border-zinc-900 font-mono">
            Diagnostics HUD
          </h2>
          
          <div class="space-y-4">
            <!-- Bitrate -->
            <div class="flex justify-between items-center py-1.5 border-b border-zinc-150 dark:border-zinc-900/50">
              <span class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono">Actual Bitrate</span>
              <div class="flex items-baseline gap-1">
                <span class="font-mono text-lg font-semibold text-zinc-850 dark:text-white tabular-nums">
                  {casting ? encodeBitrate : '—'}
                </span>
                {#if casting}
                  <span class="text-[9px] font-mono text-zinc-450 dark:text-zinc-500">kbps</span>
                {/if}
              </div>
            </div>

            <!-- Frame Rate -->
            <div class="flex justify-between items-center py-1.5 border-b border-zinc-150 dark:border-zinc-900/50">
              <span class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono">Encode Frame Rate</span>
              <div class="flex items-baseline gap-1">
                <span class="font-mono text-lg font-semibold text-zinc-850 dark:text-white tabular-nums">
                  {casting ? encodeFps : '—'}
                </span>
                {#if casting}
                  <span class="text-[9px] font-mono text-zinc-450 dark:text-zinc-500">FPS</span>
                {/if}
              </div>
            </div>

            <!-- Network Latency RTT -->
            <div class="flex justify-between items-center py-1.5 border-b border-zinc-150 dark:border-zinc-900/50">
              <span class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono">Network Latency</span>
              <div class="flex items-baseline gap-1">
                <span class="font-mono text-lg font-semibold text-zinc-850 dark:text-white tabular-nums">
                  {casting ? rtt : '—'}
                </span>
                {#if casting}
                  <span class="text-[9px] font-mono text-zinc-455 dark:text-zinc-500">ms RTT</span>
                {/if}
              </div>
            </div>

            <!-- Encode Duration -->
            <div class="flex justify-between items-center py-1.5 border-b border-zinc-150 dark:border-zinc-900/50">
              <span class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono">Encode Latency</span>
              <div class="flex items-baseline gap-1">
                <span class="font-mono text-lg font-semibold text-zinc-850 dark:text-white tabular-nums">
                  {casting ? encodeTime : '—'}
                </span>
                {#if casting}
                  <span class="text-[9px] font-mono text-zinc-455 dark:text-zinc-500">ms</span>
                {/if}
              </div>
            </div>

            <!-- Subscribers -->
            <div class="flex justify-between items-center py-1.5 border-b border-zinc-150 dark:border-zinc-900/50">
              <span class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono">Subscribers</span>
              <div class="flex items-baseline gap-1">
                <span class="font-mono text-lg font-semibold text-zinc-850 dark:text-white tabular-nums">{webSubscribers}</span>
                <span class="text-[9px] font-mono text-zinc-455 dark:text-zinc-500">viewers</span>
              </div>
            </div>

            <!-- Total Frames Published -->
            <div class="flex justify-between items-center py-1.5">
              <span class="text-[10px] font-medium text-zinc-500 dark:text-zinc-400 uppercase tracking-wider font-mono">Total Frames</span>
              <div class="flex items-baseline">
                <span class="font-mono text-lg font-semibold text-zinc-850 dark:text-white truncate max-w-[150px] tabular-nums">{framesPublished}</span>
              </div>
            </div>
          </div>

          <!-- WebRTC Advanced Codec Info (Conditional Footer) -->
          {#if casting}
            <div class="mt-6 pt-4 border-t border-zinc-200 dark:border-zinc-900 flex flex-col gap-2 text-[10px] font-mono text-zinc-400 dark:text-zinc-500">
              <div class="flex items-center justify-between">
                <span class="flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-zinc-300 dark:bg-zinc-700"></span>
                  Encoder: <span class="text-zinc-700 dark:text-zinc-400">{encoderInfo || 'H.264'}</span>
                </span>
                <span class="flex items-center gap-1.5">
                  Limiter: 
                  <span class="font-semibold uppercase tracking-wider {qualityLimitation === 'none' ? 'text-zinc-450 dark:text-zinc-555' : 'text-amber-600 dark:text-amber-500 status-ring-warning'}">
                    {qualityLimitation}
                  </span>
                </span>
              </div>
              <div class="flex items-center justify-between border-t border-zinc-150 dark:border-zinc-900/50 pt-2">
                <span class="flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-zinc-300 dark:bg-zinc-700"></span>
                  Mode: <span class="text-zinc-700 dark:text-zinc-400 capitalize">{selectedMode} Mode</span>
                </span>
                <span class="flex items-center gap-1.5">
                  Audio: <span class="text-zinc-700 dark:text-zinc-400">{shareAudio ? 'Enabled' : 'Disabled'}</span>
                </span>
              </div>
            </div>
          {/if}
        </div>
      </div>
    </div>
  </div>
</div>
