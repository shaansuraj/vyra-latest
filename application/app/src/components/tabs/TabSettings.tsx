import { FiBell, FiCpu, FiDatabase, FiRefreshCw } from 'solid-icons/fi';
import { FiVideo } from 'solid-icons/fi';
import { createSignal, onMount, For, Show, type Component } from 'solid-js';
import { ArkTabs } from '../../ark/ArkTabs';
import {
  cameras,
  fetchCameras,
  editCamera,
  getGlobalFPS,
  setGlobalFPS,
  runtimeSettings,
  fetchRuntimeSettings,
  updateRuntimeSettings,
  fetchKBStats,
  reindexKnowledgeBase,
  type KBStats,
} from '../../shared';

// Settings state (will be connected to server later)
interface Settings {
  webhookUrl: string;
}

const TabSettings: Component<{ onSave?: () => void; isSaving?: boolean }> = (props) => {
  const [settings, setSettings] = createSignal<Settings>({
    webhookUrl: '',
  });

  const updateSetting = <K extends keyof Settings>(key: K, value: Settings[K]) => {
    setSettings((prev) => ({ ...prev, [key]: value }));
  };

  return (
    <div class="h-full bg-neu-925/20">
      <ArkTabs
        items={[
          {
            value: 'alerts',
            label: 'Alerts',
            icon: <FiBell size={16} />,
            content: <AlertsSettings settings={settings()} updateSetting={updateSetting} />
          },
          {
            value: 'about',
            label: 'About',
            icon: <FiCpu size={16} />,
            content: <AboutSection />
          },
          {
            value: 'fps',
            label: 'Frame Rates',
            icon: <FiVideo size={16} />,
            content: <FrameRateSettings />
          },
          {
            value: 'runtime',
            label: 'Runtime',
            icon: <FiCpu size={16} />,
            content: <RuntimeTuningSettings />
          },
          {
            value: 'knowledge',
            label: 'Knowledge',
            icon: <FiDatabase size={16} />,
            content: <KnowledgeBaseSettings />
          }
        ]}
        defaultValue="alerts"
        listClass="px-6 pt-4"
      />
    </div>
  );
};

const KnowledgeBaseSettings: Component = () => {
  const [stats, setStats] = createSignal<KBStats | null>(null);
  const [loading, setLoading] = createSignal(false);
  const [reindexing, setReindexing] = createSignal(false);
  const [message, setMessage] = createSignal('');
  const [error, setError] = createSignal('');

  const refresh = async () => {
    setLoading(true);
    setError('');
    const next = await fetchKBStats();
    setStats(next);
    if (!next) setError('Could not load knowledge base stats.');
    setLoading(false);
  };

  const runReindex = async () => {
    setReindexing(true);
    setError('');
    setMessage('');
    try {
      const result = await reindexKnowledgeBase();
      const changed = (result.indexed || []).filter((item) => !item.skipped).length;
      setMessage(`Indexed ${changed} document${changed === 1 ? '' : 's'}.`);
      await refresh();
    } catch (err: any) {
      setError(String(err?.message || 'Reindex failed.'));
    } finally {
      setReindexing(false);
    }
  };

  onMount(refresh);

  const healthClass = () => {
    switch ((stats()?.embedding_health || '').toLowerCase()) {
      case 'ok':
        return 'text-emerald-300 bg-emerald-950/40 border-emerald-800/70';
      case 'partial':
        return 'text-amber-300 bg-amber-950/40 border-amber-800/70';
      case 'disabled':
        return 'text-neu-300 bg-neu-850 border-neu-700';
      default:
        return 'text-rose-300 bg-rose-950/40 border-rose-800/70';
    }
  };

  return (
    <div class="p-6 space-y-4 max-w-3xl">
      <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-4 shadow-sm">
        <div class="flex items-start justify-between gap-3">
          <div>
            <div class="text-sm font-medium text-white">Knowledge Base</div>
            <p class="text-xs text-neu-500 mt-0.5">Bundled documentation used by Ask AI.</p>
          </div>
          <div class="flex items-center gap-2">
            <button class="btn-secondary h-9 px-3" onClick={refresh} disabled={loading() || reindexing()}>
              <FiRefreshCw size={13} />
              {loading() ? 'Refreshing' : 'Refresh'}
            </button>
            <button class="btn-primary h-9 px-3" onClick={runReindex} disabled={reindexing()}>
              <FiDatabase size={13} />
              {reindexing() ? 'Reindexing' : 'Reindex Knowledge Base'}
            </button>
          </div>
        </div>

        <Show when={stats()}>
          {(s) => (
            <>
              <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
                <div class="rounded-lg border border-neu-800 bg-neu-950/45 p-3">
                  <div class="text-[11px] text-neu-500">Documents</div>
                  <div class="text-lg font-semibold text-neu-100">{s().documents || 0}</div>
                </div>
                <div class="rounded-lg border border-neu-800 bg-neu-950/45 p-3">
                  <div class="text-[11px] text-neu-500">Chunks</div>
                  <div class="text-lg font-semibold text-neu-100">{s().chunks || 0}</div>
                </div>
                <div class="rounded-lg border border-neu-800 bg-neu-950/45 p-3">
                  <div class="text-[11px] text-neu-500">Embedded</div>
                  <div class="text-lg font-semibold text-neu-100">{s().embedded_chunks || 0}</div>
                </div>
                <div class={`rounded-lg border p-3 ${healthClass()}`}>
                  <div class="text-[11px] opacity-80">Embedding Health</div>
                  <div class="text-sm font-semibold uppercase">{s().embedding_health || 'unknown'}</div>
                </div>
              </div>
              <div class="space-y-1 text-xs text-neu-500">
                <div>Model: <span class="text-neu-300">{s().stored_embedding_model || s().embedding_model || 'not recorded'}</span></div>
                <div>Dimension: <span class="text-neu-300">{s().embedding_dim || 0}</span></div>
                <div class="break-all">Endpoint: <span class="text-neu-300">{s().embedding_endpoint || 'default'}</span></div>
              </div>
              <Show when={s().last_embedding_error}>
                <div class="rounded-lg border border-rose-900/70 bg-rose-950/25 p-3 text-xs text-rose-200 break-words">
                  {s().last_embedding_error}
                </div>
              </Show>
            </>
          )}
        </Show>

        <Show when={message()}>
          <div class="rounded-lg border border-emerald-900/70 bg-emerald-950/25 p-3 text-xs text-emerald-200">{message()}</div>
        </Show>
        <Show when={error()}>
          <div class="rounded-lg border border-rose-900/70 bg-rose-950/25 p-3 text-xs text-rose-200">{error()}</div>
        </Show>
      </div>
    </div>
  );
};

// Sub-components

interface SettingsSectionProps {
  settings: Settings;
  updateSetting: <K extends keyof Settings>(key: K, value: Settings[K]) => void;
}

const AlertsSettings: Component<SettingsSectionProps> = (props) => {
  return (
    <div class="p-6 space-y-4 max-w-2xl">
      <p class="text-sm text-neu-400">
        Configure outbound notifications for high-severity safety alerts.
      </p>

      {/* Webhook — functional */}
      <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-3 shadow-sm">
        <div>
          <div class="text-sm font-medium text-white">Webhook Notifications</div>
          <p class="text-xs text-neu-500 mt-0.5">
            Receive alert payloads via HTTP POST to your endpoint.
          </p>
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-neu-400">Webhook URL</label>
          <input
            type="url"
            value={props.settings.webhookUrl}
            onInput={(e) => props.updateSetting('webhookUrl', e.currentTarget.value)}
            placeholder="https://your-domain.com/webhook"
            class="input w-full"
          />
          <p class="text-[11px] text-neu-600">
            VYRA will POST a JSON payload with event details when a high-severity alert is triggered.
          </p>
        </div>
      </div>

      {/*
        Coming-soon channels — kept in code but hidden until implemented.

        <div class="bg-neu-850 border border-neu-800 rounded-xl p-5 opacity-40 pointer-events-none select-none">
          <div class="text-sm font-medium text-white">WhatsApp Notifications</div>
          <p class="text-xs text-neu-500 mt-1">Coming soon</p>
        </div>

        <div class="bg-neu-850 border border-neu-800 rounded-xl p-5 opacity-40 pointer-events-none select-none">
          <div class="text-sm font-medium text-white">Email Notifications</div>
          <p class="text-xs text-neu-500 mt-1">Coming soon</p>
        </div>
      */}
    </div>
  );
};

const AboutSection: Component = () => (
  <div class="p-6 space-y-4 max-w-2xl">
    <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-4 shadow-sm">
      <div class="flex items-center gap-3">
        <div class="w-10 h-10 rounded-xl bg-violet-700 flex items-center justify-center shadow-lg shadow-violet-900/40">
          <svg width="20" height="20" viewBox="0 0 16 16" fill="none">
            <circle cx="8" cy="8" r="2.5" fill="white"/>
            <circle cx="8" cy="8" r="5.5" stroke="white" stroke-width="1.2" stroke-dasharray="2 1.5" opacity="0.6"/>
            <circle cx="8" cy="8" r="7" stroke="white" stroke-width="1" opacity="0.3"/>
          </svg>
        </div>
        <div>
          <div class="text-base font-bold text-white">VYRA</div>
          <div class="text-xs text-neu-500">Visual AI Risk Analysis</div>
        </div>
      </div>

      <div class="border-t border-neu-800/60 my-2" />

      <div class="grid grid-cols-2 gap-4 text-sm">
        <div class="space-y-0.5">
          <div class="text-neu-500 text-xs">PPE Model</div>
          <div class="text-neu-200 text-sm">vyra-yolo-ppe-detection</div>
          <div class="text-neu-500 text-xs">YOLOv8m · 14-class unified PPE</div>
        </div>
        <div class="space-y-0.5">
          <div class="text-neu-500 text-xs">Vision-Language Model</div>
          <div class="text-neu-200 text-sm">SmolVLM2-2.2B-Instruct</div>
          <div class="text-neu-500 text-xs">Scene understanding & summaries</div>
        </div>
        <div class="space-y-0.5">
          <div class="text-neu-500 text-xs">Tracking</div>
          <div class="text-neu-200 text-sm">ByteTrack</div>
          <div class="text-neu-500 text-xs">Persistent person IDs · 15 FPS baseline</div>
        </div>
        <div class="space-y-0.5">
          <div class="text-neu-500 text-xs">Object Detection</div>
          <div class="text-neu-200 text-sm">YOLOv8m</div>
          <div class="text-neu-500 text-xs">80-class COCO detection</div>
        </div>
      </div>
    </div>
  </div>
);

const FrameRateSettings: Component = () => {
  const [globalFps, setGlobalFpsLocal] = createSignal(15);
  const [globalSaving, setGlobalSaving] = createSignal(false);
  const [globalSaved, setGlobalSaved] = createSignal(false);

  onMount(async () => {
    fetchCameras();
    const fps = await getGlobalFPS();
    setGlobalFpsLocal(Math.round(fps * 10) / 10);
  });

  const saveGlobalFps = async () => {
    setGlobalSaving(true);
    const result = await setGlobalFPS(globalFps());
    setGlobalSaving(false);
    if (result) {
      setGlobalFpsLocal(Number(result.fps || globalFps()));
      await fetchCameras();
      setGlobalSaved(true);
      setTimeout(() => setGlobalSaved(false), 2000);
    }
  };

  const saveCameraFPS = async (cameraId: string, processingFPS: number, vlmFPS: number) => {
    const cam = cameras().find((c) => c.id === cameraId);
    if (!cam) return;
    await editCamera(cameraId, {
      name: cam.name,
      addr: cam.addr,
      port: cam.port,
      path: cam.path,
      processing_fps: processingFPS,
      vlm_fps: vlmFPS,
    });
    await fetchCameras();
  };

  return (
    <div class="p-6 space-y-6 max-w-4xl">

      {/* All-camera detection FPS control */}
      <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-3 shadow-sm">
        <div>
          <div class="text-sm font-medium text-white">Detection FPS Across All Cameras</div>
          <p class="text-xs text-neu-500 mt-0.5">
            Applies the same detection FPS to every camera setting. Streams update immediately, and increasing FPS may restart a stream so FFmpeg can switch to the new rate.
          </p>
        </div>
        <div class="flex items-center gap-3">
          <div class="flex-1 max-w-xs">
            <label class="text-[11px] text-neu-500 mb-1 block">Detection FPS (recommended 15 – 20)</label>
            <input
              type="number"
              min="15"
              max="30"
              step="1"
              value={globalFps()}
              onInput={(e) => setGlobalFpsLocal(Number(e.currentTarget.value))}
              class="input w-full text-sm"
            />
          </div>
          <div class="pt-4">
            <button
              class="btn btn-primary"
              disabled={globalSaving()}
              onClick={saveGlobalFps}
            >
              {globalSaving() ? 'Saving…' : globalSaved() ? 'Saved!' : 'Apply'}
            </button>
          </div>
          <div class="pt-4 text-xs text-neu-500">
            Target interval: {globalFps() > 0 ? (1000 / globalFps()).toFixed(0) : '—'} ms/frame
          </div>
        </div>
      </div>

      {/* Per-camera FPS controls */}
      <div class="space-y-2">
        <p class="text-sm text-neu-400">Per-camera overrides. Each camera&apos;s <span class="font-mono">processing_fps</span> now drives extraction directly. Use 15 FPS as the baseline for PPE and Person detection unless a camera genuinely needs more.</p>
        <For each={cameras()}>
          {(cam) => {
            let procRef: HTMLInputElement | undefined;
            let vlmRef: HTMLInputElement | undefined;
            return (
              <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-4 grid grid-cols-1 md:grid-cols-5 gap-3 items-center shadow-sm">
                <div class="md:col-span-2">
                  <div class="text-sm text-white font-medium">{cam.name}</div>
                  <div class="text-[11px] text-neu-600 font-mono">{cam.id.slice(0, 12)}...</div>
                </div>
                <div>
                  <label class="text-[11px] text-neu-500">Detection FPS</label>
                  <input ref={procRef} type="number" min="15" max="30" step="1" value={cam.processing_fps ?? 15}
                    class="input w-full text-sm" />
                </div>
                <div>
                  <label class="text-[11px] text-neu-500">VLM FPS</label>
                  <input ref={vlmRef} type="number" min="0.2" max="10" step="0.2" value={cam.vlm_fps ?? 5}
                    class="input w-full text-sm" />
                </div>
                <button
                  class="btn btn-primary"
                  onClick={() => saveCameraFPS(cam.id, Number(procRef?.value || cam.processing_fps || 15), Number(vlmRef?.value || cam.vlm_fps || 5))}
                >
                  Save
                </button>
              </div>
            );
          }}
        </For>
      </div>
    </div>
  );
};

const RuntimeTuningSettings: Component = () => {
  const [ppeAssoc, setPpeAssoc] = createSignal(true);
  const [snapCooldown, setSnapCooldown] = createSignal(30);
  const [reidThreshold, setReidThreshold] = createSignal(0.84);
  const [llmModel, setLlmModel] = createSignal('google/gemma-3-1b-it');
  const [saving, setSaving] = createSignal(false);
  const [saved, setSaved] = createSignal(false);

  const hydrate = async () => {
    const s = await fetchRuntimeSettings();
    const cur = s || runtimeSettings();
    setPpeAssoc(Boolean(cur.ppe_require_person_association));
    setSnapCooldown(Number(cur.snapshot_cooldown_seconds || 0));
    setReidThreshold(Number(cur.person_reid_match_threshold || 0.84));
    setLlmModel(String(cur.llm_model_id || 'google/gemma-3-1b-it'));
  };

  onMount(hydrate);

  const save = async () => {
    setSaving(true);
    const out = await updateRuntimeSettings({
      ppe_require_person_association: ppeAssoc(),
      snapshot_cooldown_seconds: snapCooldown(),
      person_reid_match_threshold: reidThreshold(),
      llm_model_id: llmModel().trim(),
      restart_llm_if_running: true,
    });
    setSaving(false);
    if (out) {
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    }
  };

  return (
    <div class="p-6 space-y-6 max-w-3xl">
      <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-4 shadow-sm">
        <div>
          <div class="text-sm font-medium text-white">PPE Violation Behavior</div>
          <p class="text-xs text-neu-500 mt-0.5">
            Controls how strict PPE violation association and snapshot cadence should be.
          </p>
        </div>

        <label class="flex items-center justify-between gap-3">
          <div>
            <div class="text-sm text-neu-200">Require person association for NO-* labels</div>
            <div class="text-[11px] text-neu-600">
              If disabled, high-confidence NO-* can still trigger violation events without person box association.
            </div>
          </div>
          <input
            type="checkbox"
            class="w-4 h-4"
            checked={ppeAssoc()}
            onChange={(e) => setPpeAssoc(e.currentTarget.checked)}
          />
        </label>

        <div>
          <label class="text-[11px] text-neu-500">Snapshot cooldown seconds (0 = every violation)</label>
          <input
            type="number"
            min="0"
            step="1"
            class="input w-44 mt-1"
            value={snapCooldown()}
            onInput={(e) => setSnapCooldown(Number(e.currentTarget.value))}
          />
        </div>
      </div>

      <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-4 shadow-sm">
        <div>
          <div class="text-sm font-medium text-white">Identity Matching</div>
          <p class="text-xs text-neu-500 mt-0.5">
            Tune unknown-to-unknown ReID matching confidence threshold.
          </p>
        </div>
        <div>
          <label class="text-[11px] text-neu-500">Person ReID match threshold (0.50 - 0.99)</label>
          <input
            type="number"
            min="0.5"
            max="0.99"
            step="0.01"
            class="input w-44 mt-1"
            value={reidThreshold()}
            onInput={(e) => setReidThreshold(Number(e.currentTarget.value))}
          />
        </div>
      </div>

      <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-5 space-y-4 shadow-sm">
        <div>
          <div class="text-sm font-medium text-white">Ask AI Model</div>
          <p class="text-xs text-neu-500 mt-0.5">
            Changes persist to env files and restart Ask-AI worker when currently running.
          </p>
        </div>
        <div>
          <label class="text-[11px] text-neu-500">Model ID</label>
          <input
            type="text"
            class="input w-full mt-1"
            value={llmModel()}
            onInput={(e) => setLlmModel(e.currentTarget.value)}
            placeholder="google/gemma-3-1b-it"
          />
        </div>
      </div>

      <div class="flex items-center gap-3">
        <button class="btn btn-primary" disabled={saving()} onClick={save}>
          {saving() ? 'Saving…' : saved() ? 'Saved!' : 'Save Runtime Settings'}
        </button>
        <button class="btn btn-ghost" onClick={hydrate}>
          Reload
        </button>
      </div>
    </div>
  );
};

export default TabSettings;
