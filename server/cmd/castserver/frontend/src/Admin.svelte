<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { Card, Button, Select, Badge } from 'flowbite-svelte';
  import { makeQR } from './lib/qrcode';
  import type { Preset, Stats } from './lib/api';

  let presets: Preset[] = [];
  let selectedPreset = '';
  let fps = '30';
  let casting = false;
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
    { value: 'vp9', name: 'Force VP9 (Highly Optimized Software/HW)' },
    { value: 'vp8', name: 'Force VP8 (Highly Optimized Software/HW)' },
  ];
  let selectedCodec = 'auto';

  function qrAction(node: HTMLElement, url: string) {
    node.appendChild(makeQR(url, 4));
    return {
      update(u: string) { node.innerHTML = ''; node.appendChild(makeQR(u, 4)); },
      destroy() { node.innerHTML = ''; },
    };
  }

  function qrMini(node: HTMLElement, url: string) {
    node.appendChild(makeQR(url, 2));
    return {
      update(u: string) { node.innerHTML = ''; node.appendChild(makeQR(u, 2)); },
      destroy() { node.innerHTML = ''; },
    };
  }

  function setCtrl(msg: string, err: boolean) {
    ctrlMsg = msg;
    ctrlErr = err;
  }

  function castCleanup() {
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

    setCtrl('Requesting screen capture...', false);
    try {
      const s = await navigator.mediaDevices.getDisplayMedia({
        video: {
          width: p.width > 0 ? { ideal: p.width } : undefined,
          height: p.height > 0 ? { ideal: p.height } : undefined,
          frameRate: { ideal: fpsVal },
          resizeMode: 'none',
        } as MediaTrackConstraints,
        audio: false,
      });
      castStream = s;
      s.getAudioTracks().forEach(t => s.removeTrack(t));

      if (castPreview) {
        castPreview.srcObject = castStream;
        castPreview.style.display = 'block';
      }

      castStream.getVideoTracks()[0].onended = () => { doStop(); };

      setCtrl('Connecting signaling channel...', false);

      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = proto + '//' + window.location.host + '/webrtc/publish';

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

      const tracks = castStream!.getVideoTracks();
      tracks.forEach(track => {
        if ('contentHint' in track) {
          track.contentHint = 'detail';
        }
        const sender = pc!.addTrack(track, castStream!);

        // Apply selected codec preference
        if (selectedCodec !== 'auto' && typeof RTCRtpSender !== 'undefined' && 'getCapabilities' in RTCRtpSender) {
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

            if (p.bitrate > 0) {
              setTimeout(() => {
                const sender = pc?.getSenders().find(s => s.track?.kind === 'video');
                if (sender) {
                  const params = sender.getParameters();
                  if (!params.encodings) params.encodings = [{}];
                  params.encodings[0].maxBitrate = p.bitrate * 1000;
                  params.degradationPreference = 'maintain-resolution'; // Prioritize resolution crispness over FPS if bandwidth fluctuates
                  sender.setParameters(params).catch(() => {});
                  console.log('Applied encoder parameters: maxBitrate =', p.bitrate * 1000, 'degradationPreference = maintain-resolution');
                }
              }, 500);
            }

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
          const dt = (activeOutbound.timestamp - lastTime) / 1000; // seconds
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

  function waitForIceGathering(pc: RTCPeerConnection): Promise<void> {
    return new Promise((resolve) => {
      if (pc.iceGatheringState === 'complete') {
        resolve();
        return;
      }
      const timer = setTimeout(() => {
        console.log('publisher ICE gathering timeout, proceeding with current candidates');
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

  async function copyUrl(url: string) {
    try {
      await navigator.clipboard.writeText(url);
    } catch { }
  }

  async function loadPresets() {
    try {
      const list: Preset[] = await (await fetch('/presets')).json();
      presets = list;
      if (list.length > 0) {
        // Default to the "Native" preset for maximum quality
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

<div class="min-h-screen bg-gray-950 text-gray-200 p-4 md:p-6">
  <div class="mx-auto max-w-5xl">
    <header class="flex items-center justify-between pb-4 mb-6 border-b border-gray-800">
      <h1 class="text-xl font-semibold tracking-tight">castserver</h1>
      <div class="flex items-center gap-2">
        <Badge color={online ? 'green' : 'red'} rounded>
          {online ? 'Online' : 'Checking...'}
        </Badge>
        <Badge color="blue" rounded>WebRTC</Badge>
      </div>
    </header>

    <div class="grid grid-cols-[1.2fr_1fr] gap-6 max-md:grid-cols-1">
      <div>
        <Card size="none">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-5 pb-2 border-b border-gray-700">
            Cast Configuration
          </h2>

          <div class="mb-4">
            <Select items={presetItems} bind:value={selectedPreset} disabled={casting} placeholder="Select preset" size="md" />
          </div>

          <div class="mb-4">
            <Select items={fpsItems} bind:value={fps} disabled={casting} placeholder="FPS" size="md" />
          </div>

          <div class="mb-4">
            <Select items={codecItems} bind:value={selectedCodec} disabled={casting} placeholder="Select Codec" size="md" />
          </div>

          <div class="flex gap-3 mt-5">
            <Button on:click={doStart} disabled={casting}>Share Screen</Button>
            <Button color="red" on:click={doStop} disabled={!casting}>Stop</Button>
          </div>

          <p class="text-xs mt-4" class:text-green-400={!ctrlErr} class:text-red-400={ctrlErr}>
            {ctrlMsg || ''}
          </p>
        </Card>

        <video
          bind:this={castPreview}
          autoplay muted playsinline
          class="w-full rounded-lg border border-gray-700 mt-4 hidden bg-black"
        ></video>
      </div>

      <div>
        <Card size="none">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-5 pb-2 border-b border-gray-700">
            Quick Share
          </h2>
          <div class="flex flex-col gap-3">
            {#if networkIps.length === 0}
              <p class="text-xs text-gray-500 text-center py-4">No network interfaces found.</p>
            {:else if networkIps.length === 1}
              {@const url = viewerUrl(networkIps[0])}
              <div class="flex flex-col items-center gap-4 p-5 bg-gray-900/50 rounded-lg border border-gray-800">
                <div use:qrAction={url} class="bg-white p-2 rounded-lg shadow-lg"></div>
                <div class="flex items-center w-full bg-gray-950 border border-gray-800 rounded-lg overflow-hidden">
                  <span class="flex-1 font-mono text-xs p-2.5 truncate text-gray-400">{url}</span>
                  <button
                    on:click={() => copyUrl(url)}
                    class="bg-gray-800 hover:bg-gray-700 border-l border-gray-700 px-3.5 py-2.5 text-xs font-semibold text-gray-200 transition-colors"
                  >Copy</button>
                </div>
              </div>
            {:else}
              {#each networkIps as ip}
                {@const url = viewerUrl(ip)}
                <div class="flex items-center gap-3 p-3 bg-gray-900/50 rounded-lg border border-gray-800">
                  <div use:qrMini={url} class="bg-white p-1 rounded shrink-0"></div>
                  <div class="flex-1 min-w-0">
                    <div class="font-mono text-xs text-gray-300 truncate">{url}</div>
                  </div>
                  <button
                    on:click={() => copyUrl(url)}
                    class="shrink-0 bg-gray-800 hover:bg-gray-700 px-3 py-1.5 rounded text-xs font-semibold text-gray-200 transition-colors"
                  >Copy</button>
                </div>
              {/each}
            {/if}
          </div>
        </Card>

        <Card size="none">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-5 pb-2 border-b border-gray-700">
            Session Stats
          </h2>
          <table class="w-full text-sm">
            <thead>
              <tr class="text-gray-400 font-medium border-b border-gray-800">
                <th class="py-2.5 text-left">Metric</th>
                <th class="py-2.5 text-right">Value</th>
              </tr>
            </thead>
            <tbody>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5">Frames Published</td>
                <td class="py-2.5 text-right font-mono font-semibold">{framesPublished}</td>
              </tr>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5">Subscribers</td>
                <td class="py-2.5 text-right font-mono font-semibold">{webSubscribers}</td>
              </tr>
              <tr>
                <td class="py-2.5">Session State</td>
                <td
                  class="py-2.5 text-right font-mono font-semibold"
                  style="color: {sessionActive ? '#10b981' : '#6b7280'}"
                >{sessionActive ? 'active' : 'idle'}</td>
              </tr>
            </tbody>
          </table>
        </Card>

        {#if casting}
        <Card size="none" class="mt-4">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-gray-400 mb-5 pb-2 border-b border-gray-700 flex justify-between items-center">
            <span>WebRTC Telemetry</span>
            <Badge color="blue" class="text-[10px]">Active</Badge>
          </h2>
          <table class="w-full text-sm">
            <thead>
              <tr class="text-gray-400 font-medium border-b border-gray-800">
                <th class="py-2.5 text-left">Pipeline Stage</th>
                <th class="py-2.5 text-right">Value</th>
              </tr>
            </thead>
            <tbody>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5 flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-blue-500"></span>
                  Encoder Codec
                </td>
                <td class="py-2.5 text-right font-mono font-semibold text-gray-300">{encoderInfo || 'H.264'}</td>
              </tr>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5 flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-violet-500"></span>
                  Encode Latency
                </td>
                <td class="py-2.5 text-right font-mono font-semibold text-violet-400">{encodeTime} ms</td>
              </tr>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5 flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-emerald-500"></span>
                  Network Latency (RTT)
                </td>
                <td class="py-2.5 text-right font-mono font-semibold text-emerald-400">{rtt} ms</td>
              </tr>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5 flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-amber-500"></span>
                  Actual Bitrate
                </td>
                <td class="py-2.5 text-right font-mono font-semibold text-amber-400">{encodeBitrate} kbps</td>
              </tr>
              <tr class="border-b border-gray-800/50">
                <td class="py-2.5 flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-indigo-500"></span>
                  Frame Rate
                </td>
                <td class="py-2.5 text-right font-mono font-semibold text-indigo-400">{encodeFps} FPS</td>
              </tr>
              <tr>
                <td class="py-2.5 flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full bg-rose-500"></span>
                  Quality Limiter
                </td>
                <td class="py-2.5 text-right font-mono font-semibold" class:text-green-400={qualityLimitation === 'none'} class:text-rose-400={qualityLimitation !== 'none'}>
                  {qualityLimitation}
                </td>
              </tr>
            </tbody>
          </table>
        </Card>
        {/if}
      </div>
    </div>
  </div>
</div>
