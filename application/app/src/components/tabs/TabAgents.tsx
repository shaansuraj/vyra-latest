import { Show, For, batch, createMemo, createSignal, onMount, createEffect, type Component } from 'solid-js';
import { FiShield, FiUsers, FiEye, FiPlay, FiSquare, FiRefreshCw, FiUser, FiZap, FiUpload, FiAlertTriangle } from 'solid-icons/fi';
import {
  agents,
  fetchAgents,
  fetchNodes,
  systemStatus,
  fetchSystemStatus,
  systemWorkers,
  fetchSystemWorkers,
  scaleSystemWorkers,
  autoScalingEnabled,
  autoScalingMax,
  setSystemAutoScaling,
  syncPredefinedAgents,
  createAgent,
  createCustomYoloAgent,
  startCustomAgentRuntime,
  scaleCustomAgentRuntime,
  stopCustomAgentRuntime,
  customWorkersStatus,
  fetchCustomWorkersStatus,
  llmWorkerStatus,
  fetchLLMWorkerStatus,
  startLLMWorker,
  stopLLMWorker,
  systemRuntimeMetrics,
  type AgentInfo,
} from '../../shared';
import { toaster } from '../../ark/ArkToast';

type WorkerType = 'ppe' | 'objdet' | 'vlm' | 'face';
const WORKER_TYPES: WorkerType[] = ['ppe', 'objdet', 'vlm', 'face'];

type AgentDef = {
  id: string;
  name: string;
  description: string;
  workerType: WorkerType;
  dependencies: WorkerType[];
};

const AGENTS: AgentDef[] = [
  {
    id: 'ppe',
    name: 'PPE Detection',
    description: 'Detect PPE violations in real time, save violation snapshots, and attach VLM brief + AI summary.',
    workerType: 'ppe',
    dependencies: ['vlm', 'face'],
  },
  {
    id: 'person',
    name: 'Person Detection',
    description: 'Track persons with persistent IDs, shared face recognition, and save visit snapshots with VLM brief.',
    workerType: 'objdet',
    dependencies: ['vlm', 'face'],
  },
  {
    id: 'situational',
    name: 'Situational Awareness',
    description: 'VLM-only monitoring pipeline at reduced FPS, persisting only problematic frames with summaries.',
    workerType: 'vlm',
    dependencies: [],
  },
];

type PoolDef = {
  type: WorkerType;
  name: string;
  shortName: string;
  sharedBy: string[];
};

const WORKER_POOLS: PoolDef[] = [
  { type: 'ppe', name: 'PPE Detection Workers', shortName: 'PPE', sharedBy: ['PPE Detection'] },
  { type: 'objdet', name: 'Object Detection Workers', shortName: 'ObjDet', sharedBy: ['Person Detection'] },
  { type: 'vlm', name: 'VLM Workers', shortName: 'VLM', sharedBy: ['PPE Detection', 'Person Detection', 'Situational Awareness'] },
  { type: 'face', name: 'Face Identity Workers', shortName: 'Face ID', sharedBy: ['PPE Detection', 'Person Detection'] },
];

const workerChip = (status: 'running' | 'partial' | 'stopped') => {
  if (status === 'running') return 'bg-green-900/40 text-green-300 border-green-700/40';
  if (status === 'partial') return 'bg-amber-900/40 text-amber-300 border-amber-700/40';
  return 'bg-neu-800 text-neu-400 border-neu-700';
};

const clampReplica = (value: number) => {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(16, Math.round(value)));
};

const clampPct = (value: number) => {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, Math.round(value)));
};

const statusLabel = (status: 'running' | 'partial' | 'stopped') => {
  if (status === 'running') return 'Running';
  if (status === 'partial') return 'Starting...';
  return 'Stopped';
};

const agentIcon = (type: WorkerType) => {
  if (type === 'ppe') return <FiShield size={18} class="text-orange-400" />;
  if (type === 'objdet') return <FiUsers size={18} class="text-blue-400" />;
  return <FiEye size={18} class="text-violet-400" />;
};

const poolIcon = (type: WorkerType) => {
  if (type === 'ppe') return <FiShield size={16} class="text-orange-400" />;
  if (type === 'objdet') return <FiUsers size={16} class="text-blue-400" />;
  if (type === 'face') return <FiUser size={16} class="text-emerald-400" />;
  return <FiEye size={16} class="text-violet-400" />;
};

const workersRunning = (type: WorkerType) => systemWorkers()[type]?.workers?.filter((w) => w.running).length ?? 0;
const adaptersRunning = (type: WorkerType) => systemWorkers()[type]?.adapters?.filter((w) => w.running).length ?? 0;
const desiredReplicas = (type: WorkerType) => systemWorkers()[type]?.desired ?? 0;
const queueFillPct = (type: WorkerType) => clampPct(systemWorkers()[type]?.queue_fill_pct ?? 0);
const totalDropped = (type: WorkerType) => systemWorkers()[type]?.total_dropped ?? 0;
const connectedWorkers = (type: WorkerType) => systemWorkers()[type]?.connected_workers ?? 0;
const poolReason = (type: WorkerType) => systemWorkers()[type]?.reason ?? '';

const poolState = (type: WorkerType): 'running' | 'partial' | 'stopped' => {
  const d = desiredReplicas(type);
  const w = workersRunning(type);
  const a = adaptersRunning(type);
  if (d > 0 && w >= d && a >= d) return 'running';
  if (w > 0 || a > 0) return 'partial';
  return 'stopped';
};

const AgentCard: Component<{ def: AgentDef }> = (props) => {
  const engineRunning = () => !!systemStatus()['engine']?.running;
  const desired = () => desiredReplicas(props.def.workerType);
  const running = () => workersRunning(props.def.workerType);
  const canStop = createMemo(() => running() > 0 || desired() > 0);

  const state = createMemo<'running' | 'partial' | 'stopped'>(() => {
    if (!engineRunning()) return 'stopped';
    return poolState(props.def.workerType);
  });
  const canStart = createMemo(() => state() === 'stopped');

  const handleStart = async () => {
    const target = Math.max(1, desired() || 1);
    const result = await scaleSystemWorkers(props.def.workerType, target);
    if (result.ok) toaster.success({ title: `${props.def.name} started`, description: `Worker pool scaled to ${target}.` });
    else toaster.error({ title: 'Failed to start', description: result.error || 'Could not start worker pool.' });
  };

  const handleStop = async () => {
    const result = await scaleSystemWorkers(props.def.workerType, 0);
    if (result.ok) toaster.success({ title: `${props.def.name} stopped`, description: 'All replicas stopped.' });
    else toaster.error({ title: 'Failed', description: result.error || 'Could not stop workers.' });
  };

  return (
    <div class="bg-neu-900/75 border border-neu-800/80 rounded-2xl p-5 shadow-sm hover:border-violet-700/40 transition-colors">
      <div class="flex items-start justify-between gap-3 mb-3">
        <div class="flex items-center gap-3">
          <div class="w-10 h-10 rounded-xl border border-neu-700/80 bg-neu-950/80 flex items-center justify-center">
            {agentIcon(props.def.workerType)}
          </div>
          <div>
            <div class="text-sm font-semibold text-white">{props.def.name}</div>
            <span class={`inline-flex mt-1 px-2 py-0.5 rounded-full text-[10px] border font-semibold ${workerChip(state())}`}>
              {statusLabel(state())}
            </span>
          </div>
        </div>
      </div>

      <p class="text-xs text-neu-500 leading-relaxed mb-3">{props.def.description}</p>

      <Show when={props.def.dependencies.length > 0}>
        <div class="flex flex-wrap items-center gap-1.5 mb-4">
          <span class="text-[10px] text-neu-600 mr-1">Uses:</span>
          <span class="px-2 py-0.5 rounded-full text-[10px] border border-neu-700 bg-neu-900 text-neu-400 font-medium">
            {props.def.workerType.toUpperCase()} workers
          </span>
          <For each={props.def.dependencies}>
            {(dep) => (
              <span class={`px-2 py-0.5 rounded-full text-[10px] border font-medium ${
                poolState(dep) === 'running' ? 'border-green-700/40 bg-green-900/20 text-green-400' :
                poolState(dep) !== 'stopped' ? 'border-amber-700/40 bg-amber-900/20 text-amber-400' :
                'border-neu-700 bg-neu-900 text-neu-400'
              }`}>
                {dep.toUpperCase()} workers
              </span>
            )}
          </For>
        </div>
      </Show>

      <div class="flex items-center gap-2">
        <button
          class={`flex-1 flex items-center justify-center gap-2 py-2 rounded-xl text-xs font-semibold border ${
            canStart()
              ? 'bg-neu-850 hover:bg-neu-800 border-neu-700/80 text-neu-100'
              : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
          }`}
          onClick={handleStart}
          disabled={!canStart()}
        >
          <FiPlay size={12} /> Start
        </button>
        <button
          class={`flex-1 flex items-center justify-center gap-2 py-2 rounded-xl text-xs font-semibold border ${
            canStop()
              ? 'bg-red-900/30 hover:bg-red-900/50 border-red-800/40 text-red-300'
              : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
          }`}
          onClick={handleStop}
          disabled={!canStop()}
        >
          <FiSquare size={12} /> Stop
        </button>
      </div>
    </div>
  );
};

const WorkerPoolCard: Component<{
  pool: PoolDef;
  replicas: number;
  onSetReplicas: (n: number) => void;
  onApplyScale: (target: number) => Promise<boolean>;
}> = (props) => {
  const state = createMemo(() => poolState(props.pool.type));
  const desired = () => desiredReplicas(props.pool.type);
  const running = () => workersRunning(props.pool.type);
  const queuePct = () => clampPct(queueFillPct(props.pool.type));
  const dropped = () => totalDropped(props.pool.type);
  const connected = () => connectedWorkers(props.pool.type);
  const reason = () => poolReason(props.pool.type);
  const [applying, setApplying] = createSignal(false);
  const pendingChanged = createMemo(() => clampReplica(props.replicas) !== desired());

  const applyScale = async () => {
    if (applying() || !pendingChanged()) return;
    const target = clampReplica(props.replicas);
    setApplying(true);
    await props.onApplyScale(target);
    setApplying(false);
  };

  const pressureColor = () =>
    queuePct() >= 80 ? 'text-red-400' : queuePct() >= 50 ? 'text-amber-400' : 'text-green-400';
  const pressureBarColor = () =>
    queuePct() >= 80 ? 'bg-red-500' : queuePct() >= 50 ? 'bg-amber-500' : 'bg-green-500';

  return (
    <div class="bg-neu-900/75 border border-neu-800/80 rounded-xl p-4 shadow-sm">
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          {poolIcon(props.pool.type)}
          <span class="text-xs font-semibold text-neu-200">{props.pool.name}</span>
          <span class={`px-1.5 py-0.5 rounded-full text-[9px] border font-semibold ${workerChip(state())}`}>
            {state() === 'running' ? `${running()}/${desired()}` : state() === 'partial' ? `${running()}/${desired()}` : 'off'}
          </span>
        </div>
        <Show when={connected() > 0}>
          <span class="text-[10px] text-neu-500">{connected()} connected</span>
        </Show>
      </div>

      <Show when={connected() > 0}>
        <div class="mb-2 text-xs">
          <div class="flex items-center justify-between mb-1">
            <span class="text-neu-500 text-[10px]">Queue pressure</span>
            <span class={`font-mono text-[10px] font-semibold ${pressureColor()}`}>
              {queuePct()}%
            </span>
          </div>
          <div class="w-full h-1 rounded-full bg-neu-800 overflow-hidden">
            <div class={`h-1 rounded-full transition-all duration-500 ${pressureBarColor()}`} style={`width: ${queuePct()}%`} />
          </div>
        </div>
      </Show>

      <Show when={dropped() > 0}>
        <div class="mb-2 text-[10px] text-red-300 bg-red-900/20 border border-red-700/30 rounded-lg px-2 py-1.5">
          {dropped().toLocaleString()} dropped
        </div>
      </Show>

      <Show when={reason().trim().length > 0}>
        <div class="mb-2 text-[10px] text-neu-400 bg-neu-900/80 border border-neu-800 rounded-lg px-2 py-1.5 leading-relaxed">
          {reason()}
        </div>
      </Show>

      <div class="flex items-center gap-1.5">
        <button class="w-7 h-7 flex items-center justify-center rounded bg-neu-800 hover:bg-neu-700 text-neu-300 text-xs" onClick={() => props.onSetReplicas(clampReplica(props.replicas - 1))}>-</button>
        <input
          class="w-10 text-center rounded bg-neu-900 border border-neu-700 text-neu-200 text-xs py-1"
          value={props.replicas}
          onInput={(e) => props.onSetReplicas(clampReplica(Number(e.currentTarget.value) || 0))}
        />
        <button class="w-7 h-7 flex items-center justify-center rounded bg-neu-800 hover:bg-neu-700 text-neu-300 text-xs" onClick={() => props.onSetReplicas(clampReplica(props.replicas + 1))}>+</button>
        <button
          class={`ml-auto px-2.5 py-1 rounded border text-[10px] font-medium ${
            pendingChanged() && !applying()
              ? 'bg-violet-700/40 hover:bg-violet-700/60 border-violet-700/40 text-violet-200'
              : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
          }`}
          onClick={applyScale}
          disabled={!pendingChanged() || applying()}
        >
          {applying() ? 'Applying...' : pendingChanged() ? 'Apply' : 'Synced'}
        </button>
      </div>

      <div class="mt-2 text-[9px] text-neu-600 leading-tight">
        Shared by: {props.pool.sharedBy.join(', ')}
      </div>
    </div>
  );
};

export default function TabAgents() {
  const [replicas, setReplicas] = createSignal<Record<WorkerType, number>>({ ppe: 0, objdet: 0, vlm: 0, face: 0 });
  const [replicasDirty, setReplicasDirty] = createSignal<Record<WorkerType, boolean>>({ ppe: false, objdet: false, vlm: false, face: false });
  const [customReplicas, setCustomReplicas] = createSignal<Record<string, number>>({});
  const [creatingCustom, setCreatingCustom] = createSignal(false);
  const [customName, setCustomName] = createSignal('');
  const [customInstruction, setCustomInstruction] = createSignal('');
  const [customModel, setCustomModel] = createSignal<File | null>(null);

  const [startDialogAgent, setStartDialogAgent] = createSignal<AgentInfo | null>(null);
  const [startDialogReplicas, setStartDialogReplicas] = createSignal(1);
  const [startDialogEnableVLM, setStartDialogEnableVLM] = createSignal(false);
  const [startDialogEnableFace, setStartDialogEnableFace] = createSignal(false);

  const refreshAll = async () => {
    await Promise.all([
      syncPredefinedAgents(),
      fetchAgents(),
      fetchNodes(),
      fetchSystemStatus(),
      fetchSystemWorkers(),
      fetchCustomWorkersStatus(),
      fetchLLMWorkerStatus(),
    ]);
    setReplicasDirty({ ppe: false, objdet: false, vlm: false, face: false });
  };

  onMount(async () => {
    await refreshAll();
  });

  createEffect(() => {
    const w = systemWorkers();
    const desiredByType: Record<WorkerType, number> = {
      ppe: clampReplica(w.ppe?.desired ?? 0),
      objdet: clampReplica(w.objdet?.desired ?? 0),
      vlm: clampReplica(w.vlm?.desired ?? 0),
      face: clampReplica(w.face?.desired ?? 0),
    };

    const currentReplicas = replicas();
    const dirty = replicasDirty();
    const nextReplicas = { ...currentReplicas };
    const nextDirty = { ...dirty };
    let replicasChanged = false;
    let dirtyChanged = false;

    for (const type of WORKER_TYPES) {
      const desired = desiredByType[type];
      if (dirty[type]) {
        if (currentReplicas[type] === desired) {
          nextDirty[type] = false;
          dirtyChanged = true;
        }
        continue;
      }
      if (currentReplicas[type] !== desired) {
        nextReplicas[type] = desired;
        replicasChanged = true;
      }
    }

    if (replicasChanged) setReplicas(nextReplicas);
    if (dirtyChanged) setReplicasDirty(nextDirty);
  });

  const customAgents = createMemo(() =>
    agents().filter((a) => (a.kind === 'custom_yolo') || a.worker_id.startsWith('vyra/custom_detection/'))
  );

  const customRuntimeMap = createMemo<Record<string, any>>(() => {
    const m: Record<string, any> = {};
    for (const s of customWorkersStatus().agents || []) m[s.agent_id] = s;
    return m;
  });

  createEffect(() => {
    const agentsList = customAgents();
    const runtimeByAgent = customRuntimeMap();
    setCustomReplicas((prev) => {
      const next: Record<string, number> = { ...prev };
      let changed = false;

      // Seed newly discovered custom agents.
      for (const agent of agentsList) {
        if (next[agent.id] != null) continue;
        const rt = runtimeByAgent[agent.id];
        next[agent.id] = rt?.desired ?? agent.custom_runtime_desired_replicas ?? 0;
        changed = true;
      }

      // Prune removed agents to keep state bounded.
      const valid = new Set(agentsList.map((a) => a.id));
      for (const id of Object.keys(next)) {
        if (valid.has(id)) continue;
        delete next[id];
        changed = true;
      }

      return changed ? next : prev;
    });
  });

  const setTypeReplicas = (type: WorkerType, count: number) => {
    const next = clampReplica(count);
    batch(() => {
      setReplicasDirty((prev) => ({ ...prev, [type]: true }));
      setReplicas((prev) => ({ ...prev, [type]: next }));
    });
  };

  const applySystemPoolScale = async (type: WorkerType, target: number): Promise<boolean> => {
    const replicasTarget = clampReplica(target);
    const result = await scaleSystemWorkers(type, replicasTarget);
    if (result.ok) {
      toaster.success({
        title: 'Scaled',
        description: `${WORKER_POOLS.find((p) => p.type === type)?.name || type.toUpperCase()} set to ${replicasTarget} replicas.`,
      });
      setReplicas((prev) => ({ ...prev, [type]: replicasTarget }));
      setReplicasDirty((prev) => ({ ...prev, [type]: false }));
      return true;
    }
    toaster.error({ title: 'Failed', description: result.error || 'Could not scale workers.' });
    return false;
  };

  const submitCustomAgent = async () => {
    const file = customModel();
    const name = customName().trim();
    const instruction = customInstruction().trim();
    if (!customName().trim()) {
      toaster.error({ title: 'Name required', description: 'Enter an agent name.' });
      return;
    }
    if (file && !file.name.toLowerCase().endsWith('.pt')) {
      toaster.error({ title: 'Invalid model', description: 'Only .pt files are supported.' });
      return;
    }

    setCreatingCustom(true);
    try {
      if (file) {
        const form = new FormData();
        form.append('name', name);
        form.append('instruction', instruction);
        form.append('model', file);

        const result = await createCustomYoloAgent(form);
        if (!result.ok) {
          toaster.error({ title: 'Create failed', description: result.error || 'Could not create custom agent.' });
          return;
        }
        toaster.success({ title: 'Custom detection agent created', description: 'Runtime is manual-start only. Use Start to launch dedicated workers.' });
      } else {
        if (!instruction) {
          toaster.error({ title: 'Instruction required', description: 'Add an instruction when creating without a model.' });
          return;
        }
        const result = await createAgent({
          name,
          instruction,
          worker_id: 'vyra/vlm',
          service_ids: [],
        });
        if (!result.ok) {
          toaster.error({ title: 'Create failed', description: result.error || 'Could not create VLM-only agent.' });
          return;
        }
        toaster.success({ title: 'VLM-only agent created', description: 'This agent runs on the shared VLM worker pool.' });
      }
    } finally {
      setCreatingCustom(false);
    }

    setCustomName('');
    setCustomInstruction('');
    setCustomModel(null);
    await refreshAll();
  };

  const openStartDialog = (agent: AgentInfo) => {
    setStartDialogAgent(agent);
    setStartDialogReplicas(Math.max(1, customReplicas()[agent.id] ?? agent.custom_runtime_desired_replicas ?? 1));
    setStartDialogEnableVLM(!!agent.custom_runtime_defaults?.enable_vlm);
    setStartDialogEnableFace(!!agent.custom_runtime_defaults?.enable_face);
  };

  const runStartDialog = async () => {
    const agent = startDialogAgent();
    if (!agent) return;
    const result = await startCustomAgentRuntime(
      agent.id,
      Math.max(1, Math.min(16, startDialogReplicas())),
      startDialogEnableVLM(),
      startDialogEnableFace(),
    );
    if (result.ok) {
      toaster.success({ title: 'Custom runtime started', description: `${agent.name} started successfully.` });
      setStartDialogAgent(null);
      await refreshAll();
    } else {
      toaster.error({ title: 'Start failed', description: result.error || 'Could not start custom runtime.' });
    }
  };

  const customAgg = createMemo(() => customWorkersStatus().aggregate);
  const llmStatus = createMemo(() => llmWorkerStatus());
  const canStartLLM = createMemo(() => !llmStatus().running);
  const canStopLLM = createMemo(() => llmStatus().running);
  const systemPoolInsights = createMemo(() =>
    WORKER_POOLS.map((pool) => {
      const desired = desiredReplicas(pool.type);
      const queue = queueFillPct(pool.type);
      const dropped = totalDropped(pool.type);
      const lagging = desired > 0 && workersRunning(pool.type) < desired;
      const suggestScale = (queue >= 70 || dropped > 0 || lagging) && desired < 16;
      return {
        ...pool,
        desired,
        queue,
        dropped,
        lagging,
        suggestScale,
      };
    })
  );
  const scaleSuggestions = createMemo(() =>
    systemPoolInsights()
      .filter((p) => p.suggestScale)
      .sort((a, b) => {
        if (b.queue !== a.queue) return b.queue - a.queue;
        return Number(b.dropped > 0) - Number(a.dropped > 0);
      })
  );
  const systemPressure = createMemo(() => {
    let maxPressure = clampPct(customAgg().max_queue_fill_pct || 0);
    for (const p of systemPoolInsights()) {
      if (p.queue > maxPressure) maxPressure = p.queue;
    }
    return maxPressure;
  });
  const heavyLoad = createMemo(() => {
    if (systemPressure() >= 80) return true;
    if ((customAgg().total_dropped || 0) > 0) return true;
    return systemPoolInsights().some((p) => p.dropped > 0);
  });

  return (
    <div class="w-full h-full overflow-y-auto p-5 space-y-6 bg-neu-925/20">
      <div class="relative overflow-hidden rounded-2xl border border-neu-800/80 bg-neu-900/70 p-5 shadow-sm">
        <div class="pointer-events-none absolute -top-14 right-0 h-44 w-44 rounded-full bg-violet-600/10 blur-2xl" />
        <div class="pointer-events-none absolute -bottom-10 left-4 h-32 w-32 rounded-full bg-cyan-500/10 blur-2xl" />
        <div class="flex items-center justify-between mb-3">
          <div>
            <h2 class="text-base font-semibold text-neu-100">Agents Runtime Control</h2>
            <p class="text-xs text-neu-500 mt-0.5">Start built-in pipelines and monitor shared dependencies from one place.</p>
          </div>
          <button class="flex items-center gap-2 px-3 py-2 rounded-lg bg-neu-850 hover:bg-neu-800 border border-neu-700/80 text-neu-200 text-xs" onClick={refreshAll}>
            <FiRefreshCw size={12} /> Sync & Refresh
          </button>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          <For each={AGENTS}>
            {(def) => <AgentCard def={def} />}
          </For>
        </div>
        <div class="mt-4 bg-neu-900/75 border border-neu-800/80 rounded-2xl p-5">
          <div class="flex items-start justify-between gap-4">
            <div>
              <div class="text-sm font-semibold text-neu-100">Ask AI Worker</div>
              <div class="text-xs text-neu-600 mt-1">Local LLM runtime for natural-language event queries.</div>
              <div class="text-[11px] mt-2">
                <span class={llmStatus().running ? 'text-green-400' : 'text-amber-400'}>
                  {llmStatus().running ? (llmStatus().ready ? 'Running' : 'Starting') : 'Stopped'}
                </span>
                <span class="text-neu-500 ml-2">Model: {llmStatus().model_id || 'google/gemma-3-1b-it'}</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <button
                class={`px-3 py-2 rounded-lg text-xs font-semibold border ${
                  canStartLLM()
                    ? 'bg-neu-850 hover:bg-neu-800 border-neu-700/80 text-neu-200'
                    : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
                }`}
                onClick={async () => {
                  const ok = await startLLMWorker('google/gemma-3-1b-it');
                  if (ok) toaster.success({ title: 'Ask AI Worker started' });
                  else toaster.error({ title: 'Failed to start Ask AI Worker' });
                }}
                disabled={!canStartLLM()}
              >
                <FiPlay size={12} class="inline mr-1" /> Start
              </button>
              <button
                class={`px-3 py-2 rounded-lg text-xs font-semibold border ${
                  canStopLLM()
                    ? 'bg-red-900/30 hover:bg-red-900/50 border-red-800/40 text-red-300'
                    : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
                }`}
                onClick={async () => {
                  const ok = await stopLLMWorker();
                  if (ok) toaster.success({ title: 'Ask AI Worker stopped' });
                  else toaster.error({ title: 'Failed to stop Ask AI Worker' });
                }}
                disabled={!canStopLLM()}
              >
                <FiSquare size={12} class="inline mr-1" /> Stop
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="bg-neu-900/75 border border-neu-800/80 rounded-2xl p-5 space-y-4 shadow-sm">
        <div>
          <h3 class="text-sm font-semibold text-neu-100">Create Custom Agent</h3>
          <p class="text-xs text-neu-500 mt-0.5">Upload a YOLO `.pt` model for dedicated detection runtime, or leave model empty to create a VLM-only instruction agent.</p>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
          <input
            class="px-3 py-2 rounded-lg bg-neu-950 border border-neu-700/80 text-sm text-neu-100"
            placeholder="Agent name"
            value={customName()}
            onInput={(e) => setCustomName(e.currentTarget.value)}
          />
          <input
            class="px-3 py-2 rounded-lg bg-neu-950 border border-neu-700/80 text-sm text-neu-100"
            placeholder="Instruction (required for VLM-only)"
            value={customInstruction()}
            onInput={(e) => setCustomInstruction(e.currentTarget.value)}
          />
        </div>

        <div class="rounded-xl border border-neu-800/80 bg-neu-950/60 p-3">
          <p class="text-[11px] text-neu-400">
            Camera binding is no longer required at creation. After creation, assign this agent per camera from
            <span class="text-violet-300 font-medium"> BirdEye → Assign Agents</span>.
          </p>
          <div class="mt-2 text-[10px] text-neu-600">
            `.pt` model {'=>'} dedicated custom runtime card below. No model {'=>'} shared VLM pool agent.
          </div>
        </div>

        <div class="flex flex-wrap items-center gap-3">
          <label class="flex items-center gap-2 px-3 py-2 rounded-lg bg-neu-950 border border-neu-700/80 text-xs text-neu-300 cursor-pointer">
            <FiUpload size={12} />
            <span>{customModel()?.name || 'Upload .pt model (optional)'}</span>
            <input
              class="hidden"
              type="file"
              accept=".pt"
              onChange={(e) => setCustomModel(e.currentTarget.files?.[0] || null)}
            />
          </label>
          <button
            class={`px-3 py-2 rounded-lg border text-xs font-semibold ${creatingCustom()
              ? 'bg-neu-950 border-neu-800 text-neu-500 cursor-not-allowed'
              : 'bg-blue-700/40 hover:bg-blue-700/60 border-blue-700/40 text-blue-200'}`}
            onClick={submitCustomAgent}
            disabled={creatingCustom()}
          >
            {creatingCustom() ? 'Creating...' : 'Create Agent'}
          </button>
        </div>
      </div>

      <div>
        <div class="flex items-center justify-between mb-3">
          <div>
            <h2 class="text-sm font-semibold text-neu-100">Custom Agents</h2>
            <p class="text-xs text-neu-500 mt-0.5">Dedicated per-agent runtimes. Manual start only.</p>
          </div>
        </div>

        <Show when={customAgents().length > 0} fallback={<div class="text-xs text-neu-600 bg-neu-900 border border-neu-800 rounded-lg p-3">No custom agents created yet.</div>}>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <For each={customAgents()}>
              {(agent) => {
                const runtime = createMemo(() => customRuntimeMap()[agent.id]);
                const runWorkers = () => runtime()?.workers?.filter((x: any) => x.running).length ?? 0;
                const runAdapters = () => runtime()?.adapters?.filter((x: any) => x.running).length ?? 0;
                const desired = () => runtime()?.desired ?? agent.custom_runtime_desired_replicas ?? 0;
                const state = createMemo<'running' | 'partial' | 'stopped'>(() => {
                  if (desired() > 0 && runWorkers() >= desired() && runAdapters() >= desired()) return 'running';
                  if (runWorkers() > 0 || runAdapters() > 0 || desired() > 0) return 'partial';
                  return 'stopped';
                });
                const canStart = createMemo(() => state() === 'stopped');
                const canStop = createMemo(() => state() !== 'stopped');

                const applyScale = async () => {
                  const target = clampReplica(customReplicas()[agent.id] ?? desired());
                  const result = await scaleCustomAgentRuntime(agent.id, target);
                  if (result.ok) {
                    toaster.success({ title: 'Scaled', description: `${agent.name} set to ${target} replicas.` });
                    await refreshAll();
                  } else {
                    toaster.error({ title: 'Scale failed', description: result.error || 'Could not scale custom runtime.' });
                  }
                };

                const stopRuntime = async () => {
                  const result = await stopCustomAgentRuntime(agent.id);
                  if (result.ok) {
                    toaster.success({ title: 'Stopped', description: `${agent.name} runtime stopped.` });
                    await refreshAll();
                  } else {
                    toaster.error({ title: 'Stop failed', description: result.error || 'Could not stop custom runtime.' });
                  }
                };

                return (
                  <div class="bg-neu-900/75 border border-neu-800/80 rounded-2xl p-4 space-y-3 shadow-sm hover:border-violet-700/35 transition-colors">
                    <div class="flex items-start justify-between gap-2">
                      <div>
                        <div class="text-sm font-semibold text-neu-100">{agent.name}</div>
                        <div class="text-[10px] text-neu-500 mt-0.5">Model: {agent.custom_model_filename || 'custom .pt'}</div>
                        <div class="text-[10px] text-neu-600 mt-0.5">
                          Assigned cameras: {agent.service_ids?.length || 0} (set from BirdEye)
                        </div>
                      </div>
                      <span class={`inline-flex px-2 py-0.5 rounded-full text-[10px] border font-semibold ${workerChip(state())}`}>
                        {statusLabel(state())}
                      </span>
                    </div>

                    <div class="grid grid-cols-2 gap-2 text-[10px] text-neu-500">
                      <div>Workers: <span class="text-neu-300">{runWorkers()}/{desired()}</span></div>
                      <div>Adapters: <span class="text-neu-300">{runAdapters()}/{desired()}</span></div>
                      <div>Queue: <span class="text-neu-300">{clampPct(runtime()?.queue_fill_pct ?? 0)}%</span></div>
                      <div>Dropped: <span class="text-neu-300">{runtime()?.total_dropped ?? 0}</span></div>
                    </div>

                    <div class="flex items-center gap-1.5">
                      <button class="w-7 h-7 flex items-center justify-center rounded bg-neu-800 hover:bg-neu-700 text-neu-300 text-xs" onClick={() => setCustomReplicas((p) => ({ ...p, [agent.id]: clampReplica((p[agent.id] ?? desired()) - 1) }))}>-</button>
                      <input
                        class="w-12 text-center rounded bg-neu-900 border border-neu-700 text-neu-200 text-xs py-1"
                        value={customReplicas()[agent.id] ?? desired()}
                        onInput={(e) => setCustomReplicas((p) => ({ ...p, [agent.id]: clampReplica(Number(e.currentTarget.value) || 0) }))}
                      />
                      <button class="w-7 h-7 flex items-center justify-center rounded bg-neu-800 hover:bg-neu-700 text-neu-300 text-xs" onClick={() => setCustomReplicas((p) => ({ ...p, [agent.id]: clampReplica((p[agent.id] ?? desired()) + 1) }))}>+</button>
                      <button class="ml-auto px-2.5 py-1 rounded bg-violet-700/40 hover:bg-violet-700/60 border border-violet-700/40 text-violet-200 text-[10px] font-medium" onClick={applyScale}>Apply</button>
                    </div>

                    <div class="flex items-center gap-2">
                      <button
                        class={`flex-1 flex items-center justify-center gap-2 py-2 rounded-xl text-xs font-semibold border ${
                          canStart()
                            ? 'bg-neu-800 hover:bg-neu-700 border-neu-700 text-neu-200'
                            : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
                        }`}
                        onClick={() => openStartDialog(agent)}
                        disabled={!canStart()}
                      >
                        <FiPlay size={12} /> Start
                      </button>
                      <button
                        class={`flex-1 flex items-center justify-center gap-2 py-2 rounded-xl text-xs font-semibold border ${
                          canStop()
                            ? 'bg-red-900/30 hover:bg-red-900/50 border-red-800/40 text-red-300'
                            : 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
                        }`}
                        onClick={stopRuntime}
                        disabled={!canStop()}
                      >
                        <FiSquare size={12} /> Stop
                      </button>
                    </div>
                  </div>
                );
              }}
            </For>
          </div>
        </Show>
      </div>

      <div>
        <div class="flex items-center justify-between mb-3">
          <div>
            <h2 class="text-sm font-semibold text-neu-100">Worker Pools</h2>
            <p class="text-xs text-neu-500 mt-0.5">Scale shared worker pools used by built-in and custom agents.</p>
          </div>
          <div class="flex items-center gap-2 flex-wrap justify-end">
            <div class="text-[10px] text-neu-400 bg-neu-900/75 border border-neu-800/80 rounded-lg px-2.5 py-2">
              <div class="font-semibold text-neu-200">Custom Workers</div>
              <div>{customAgg().running_workers}/{customAgg().total_desired} running</div>
              <div>Queue {customAgg().max_queue_fill_pct}% · Dropped {customAgg().total_dropped}</div>
            </div>
            <div class="text-[10px] text-neu-400 bg-neu-900/75 border border-neu-800/80 rounded-lg px-2.5 py-2">
              <div class="font-semibold text-neu-200">Pipeline Health</div>
              <div>Persist fail {systemRuntimeMetrics().event_persist.failure} · dropped {systemRuntimeMetrics().event_persist.dropped}</div>
              <div>Persist ok {systemRuntimeMetrics().event_persist.success}</div>
              <div>Snapshot fail {systemRuntimeMetrics().snapshot_upload.failure} · ID avg {systemRuntimeMetrics().identity_resolve.avg_latency_ms.toFixed(1)}ms</div>
              <div class="text-neu-600">Cumulative since relay start.</div>
            </div>
            <div class={`text-[10px] rounded-lg px-2.5 py-2 border ${
              heavyLoad()
                ? 'text-red-200 bg-red-900/20 border-red-700/35'
                : 'text-neu-400 bg-neu-900/75 border-neu-800/80'
            }`}>
              <div class="font-semibold">System Pressure {systemPressure()}%</div>
              <Show when={scaleSuggestions().length > 0} fallback={<div>No immediate scale-up suggestion.</div>}>
                <For each={scaleSuggestions().slice(0, 3)}>
                  {(s) => (
                    <div>
                      {s.shortName}: {s.desired} → {Math.min(16, s.desired + 1)} (queue {s.queue}%{s.dropped > 0 ? ` · dropped ${s.dropped}` : ''})
                    </div>
                  )}
                </For>
              </Show>
            </div>
            <label class="flex items-center gap-2 text-xs text-neu-300 bg-neu-900/75 border border-neu-800/80 rounded-lg px-3 py-2 cursor-pointer">
              <input
                type="checkbox"
                checked={autoScalingEnabled()}
                onChange={async (e) => {
                  const ok = await setSystemAutoScaling(e.currentTarget.checked, autoScalingMax());
                  if (!ok) toaster.error({ title: 'Autoscaling update failed' });
                }}
              />
              <FiZap size={12} class={autoScalingEnabled() ? 'text-amber-400' : 'text-neu-500'} />
              Auto-scale
            </label>
            <Show when={autoScalingEnabled()}>
              <div class="flex items-center gap-1 text-[10px] text-neu-500">
                max
                <input
                  class="w-12 text-center rounded bg-neu-900 border border-neu-700 text-neu-200 text-xs py-1"
                  value={autoScalingMax()}
                  onInput={(e) => {
                    const v = Math.max(1, clampReplica(Number(e.currentTarget.value) || 1));
                    setSystemAutoScaling(autoScalingEnabled(), v);
                  }}
                />
              </div>
            </Show>
          </div>
        </div>

        <Show when={heavyLoad()}>
          <div class="mb-3 flex items-center gap-2 rounded-lg border border-red-700/35 bg-red-900/20 px-3 py-2 text-[11px] text-red-200">
            <FiAlertTriangle size={13} />
            Heavy load detected. Apply suggested scaling or keep Auto-scale enabled to reduce queue pressure.
          </div>
        </Show>

        <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
          <For each={WORKER_POOLS}>
            {(pool) => (
              <WorkerPoolCard
                pool={pool}
                replicas={replicas()[pool.type]}
                onSetReplicas={(n) => setTypeReplicas(pool.type, n)}
                onApplyScale={(target) => applySystemPoolScale(pool.type, target)}
              />
            )}
          </For>
        </div>
      </div>

      <Show when={startDialogAgent()}>
        <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
          <div class="w-full max-w-md bg-neu-900 border border-neu-700 rounded-2xl p-4 space-y-4">
            <div>
              <div class="text-sm font-semibold text-neu-100">Start Custom Runtime</div>
              <div class="text-xs text-neu-500 mt-0.5">{startDialogAgent()?.name}</div>
            </div>

            <div class="space-y-2">
              <label class="flex items-center justify-between text-xs text-neu-300">
                <span>Replicas</span>
                <input
                  class="w-16 text-center rounded bg-neu-950 border border-neu-700 text-neu-200 text-xs py-1"
                  value={startDialogReplicas()}
                  onInput={(e) => setStartDialogReplicas(Math.max(1, Math.min(16, Number(e.currentTarget.value) || 1)))}
                />
              </label>
              <label class="flex items-center gap-2 text-xs text-neu-300">
                <input type="checkbox" checked={startDialogEnableVLM()} onChange={(e) => setStartDialogEnableVLM(e.currentTarget.checked)} />
                Enable shared VLM
              </label>
              <label class="flex items-center gap-2 text-xs text-neu-300">
                <input type="checkbox" checked={startDialogEnableFace()} onChange={(e) => setStartDialogEnableFace(e.currentTarget.checked)} />
                Enable shared Face
              </label>
            </div>

            <div class="flex items-center gap-2">
              <button class="flex-1 py-2 rounded-lg text-xs bg-neu-800 hover:bg-neu-700 border border-neu-700 text-neu-200" onClick={() => setStartDialogAgent(null)}>Cancel</button>
              <button class="flex-1 py-2 rounded-lg text-xs bg-blue-700/40 hover:bg-blue-700/60 border border-blue-700/40 text-blue-200" onClick={runStartDialog}>Start</button>
            </div>
          </div>
        </div>
      </Show>
    </div>
  );
}
