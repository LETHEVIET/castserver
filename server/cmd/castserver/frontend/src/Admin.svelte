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

  let castWs: WebSocket | null = null;
  let castRecorder: MediaRecorder | null = null;
  let castStream: MediaStream | null = null;
  let sendChain = Promise.resolve();

  let online = false;
  let framesPublished: number | string = '—';
  let webSubscribers: number | string = '—';
  let sessionActive = false;

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

  function getCastMimeType(): string | null {
    const types = ['video/webm;codecs=h264', 'video/webm;codecs=vp8', 'video/webm;codecs=vp9', 'video/webm'];
    for (const t of types) {
      if (window.MediaRecorder && MediaRecorder.isTypeSupported(t)) return t;
    }
    return null;
  }

  function castCleanup() {
    if (castRecorder && castRecorder.state !== 'inactive') {
      try { castRecorder.stop(); } catch { }
    }
    if (castStream) {
      castStream.getTracks().forEach(t => t.stop());
      castStream = null;
    }
    if (castWs) {
      try { castWs.close(); } catch { }
      castWs = null;
    }
    castRecorder = null;
    if (castPreview) {
      castPreview.style.display = 'none';
      castPreview.srcObject = null;
    }
    casting = false;
    sendChain = Promise.resolve();
  }

  async function doStart() {
    const mimeType = getCastMimeType();
    if (!mimeType) { setCtrl('MediaRecorder not supported in this browser.', true); return; }
    if (!navigator.mediaDevices || !navigator.mediaDevices.getDisplayMedia) {
      setCtrl('getDisplayMedia not available in this browser.', true);
      return;
    }

    const p = presets.find(p => p.name === selectedPreset);
    const fpsVal = parseInt(fps, 10) || p?.fps || 30;
    const bitrateBps = (p?.bitrate || 2000) * 1000;
    const chunkMs = p?.chunk_ms || 100;

    setCtrl('Requesting screen capture...', false);
    try {
      const s = await navigator.mediaDevices.getDisplayMedia({
        video: { frameRate: { ideal: fpsVal }, resizeMode: 'none' } as MediaTrackConstraints,
        audio: false,
      });
      castStream = s;
      castStream.getAudioTracks().forEach(t => castStream!.removeTrack(t));

      if (castPreview) {
        castPreview.srcObject = castStream;
        castPreview.style.display = 'block';
      }

      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = proto + '//' + window.location.host + '/ws/cast';

      setCtrl('Connecting to server...', false);
      castWs = new WebSocket(wsUrl);
      castWs.binaryType = 'arraybuffer';

      castWs.onopen = () => {
        castWs!.send(JSON.stringify({ preset: selectedPreset, fps: fpsVal }));
        casting = true;

        castRecorder = new MediaRecorder(castStream!, {
          mimeType: mimeType,
          videoBitsPerSecond: bitrateBps,
        });

        sendChain = Promise.resolve();
        castRecorder.ondataavailable = (e: BlobEvent) => {
          if (e.data.size > 0 && castWs && castWs.readyState === WebSocket.OPEN) {
            const blob = e.data;
            const ts = Date.now();
            sendChain = sendChain.then(async () => {
              const buf = await blob.arrayBuffer();
              if (!castWs || castWs.readyState !== WebSocket.OPEN) return;
              const payload = new Uint8Array(8 + buf.byteLength);
              const view = new DataView(payload.buffer);
              view.setUint32(0, Math.floor(ts / 0x100000000));
              view.setUint32(4, ts % 0x100000000);
              payload.set(new Uint8Array(buf), 8);
              castWs.send(payload.buffer);
            });
          }
        };
        castRecorder.onerror = () => {
          setCtrl('Recorder error.', true);
          castCleanup();
        };
        castRecorder.start(chunkMs);
        setCtrl('Casting ' + selectedPreset, false);
      };

      castWs.onmessage = (ev: MessageEvent) => {
        if (typeof ev.data === 'string') {
          try {
            const m = JSON.parse(ev.data);
            if (m.error) { setCtrl('Cast: ' + m.error, true); castCleanup(); }
          } catch { }
        }
      };
      castWs.onerror = () => { setCtrl('WebSocket error.', true); castCleanup(); };
      castWs.onclose = () => {
        if (castWs !== null) { setCtrl('Cast connection closed.', false); castCleanup(); }
      };
      castStream.getVideoTracks()[0].onended = () => { doStop(); };
    } catch (err: any) {
      setCtrl('Cast failed: ' + (err.message || err), true);
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
        selectedPreset = list[0].name;
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
      <Badge color={online ? 'green' : 'red'} rounded>
        {online ? 'Online' : 'Checking...'}
      </Badge>
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
                <td class="py-2.5">Web Subscribers</td>
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
      </div>
    </div>
  </div>
</div>
