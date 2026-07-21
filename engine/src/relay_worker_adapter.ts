import { encode, decode } from "cbor-x";
import { readFile } from "fs/promises";
import {
  applyDetectionExtras,
  attachIdentityMetadataFromBBoxes,
  deriveAgentType,
  resolveCustomJobOptions,
  type RelayAgentCustomYOLO,
} from "./relay_worker_adapter_logic";

type RelayMessage = { type: string; id?: string; created_at?: string; data?: any };
type EngineToServer = { type: "worker_response"; job_id: string; output: any };
type ResourceRef = { __type: "resource-ref"; id: string };

type Resource = { id: string; type: "image"; data: Uint8Array };
type RelayAgent = { id: string; name?: string; instruction: string; custom_yolo?: RelayAgentCustomYOLO };

const RELAY_WS_URL = process.env.RELAY_WS_URL ?? "ws://localhost:9020/worker/connect";
const RELAY_HTTP_BASE = process.env.RELAY_HTTP_BASE ?? "http://localhost:9020";

// IMPORTANT: This must match the worker_id set on Agents in the Relay.
const WORKER_ID = process.env.WORKER_ID ?? "vyra/vlm";

// Map worker_id -> engine worker_type. You can override with ENGINE_WORKER_TYPE.
const ENGINE_WORKER_TYPE =
  process.env.ENGINE_WORKER_TYPE ??
  (() => {
    const id = WORKER_ID.toLowerCase();
    // PPE unified worker takes priority; "helmet" also maps to ppe_detection
    if (id.includes("ppe") || id.includes("protective")) return "ppe_detection";
    if (id.includes("helmet")) return "ppe_detection";
    if (id.includes("person")) return "object_detection";
    if (id.includes("object") || id.includes("detect")) return "object_detection";
    if (id.includes("caption")) return "caption";
    if (id.includes("embedding")) return "embedding";
    return "vlm";
  })();

const BBOX_WORKER_TYPES = new Set<string>(["object_detection", "helmet_detection", "ppe_detection", "face_recognition"]);
const IS_BBOX_WORKER = BBOX_WORKER_TYPES.has(ENGINE_WORKER_TYPE);

const ENGINE_WS_URL = process.env.ENGINE_WS_URL ?? "ws://localhost:5000/ws";
const ENGINE_VERSION = process.env.ENGINE_VERSION ?? "1.0.1";
const ENGINE_TOKEN = process.env.ENGINE_TOKEN ?? "";

// For real-time bbox: 1 frame per job = fastest turnaround (result emitted immediately).
// Higher values batch more frames but delay first result.
const MAX_FRAMES_PER_JOB = Number(process.env.MAX_FRAMES_PER_JOB ?? "1");
const MAX_IN_FLIGHT = Number(process.env.MAX_IN_FLIGHT ?? "4");
const MAX_QUEUE_SIZE = Number(process.env.MAX_QUEUE_SIZE ?? (IS_BBOX_WORKER ? "4" : "48"));
const IN_FLIGHT_TIMEOUT_MS = Number(process.env.IN_FLIGHT_TIMEOUT_MS ?? (IS_BBOX_WORKER ? "15000" : "60000"));
const MAX_JOB_RETRIES = Number(process.env.MAX_JOB_RETRIES ?? "1");
const WORKER_UNAVAILABLE_BACKOFF_MS = Number(process.env.WORKER_UNAVAILABLE_BACKOFF_MS ?? "2000");
const ADAPTER_VERBOSE = process.env.ADAPTER_VERBOSE === "1";
const IS_CUSTOM_DETECTION_WORKER = WORKER_ID.toLowerCase().startsWith("vyra/custom_detection/");
const DROP_STALE_BBOX_JOBS = (process.env.DROP_STALE_BBOX_JOBS ?? "1").trim().toLowerCase() !== "0";

// Per-service submit throttle: min ms between frame submissions per camera.
// Set to ~2000 for VLM workers (PPE/wellness) so slow VLM inference doesn't cause
// cascading drops. Set to 0 (default) for fast bbox-only workers.
const MIN_SUBMIT_INTERVAL_MS = Number(process.env.MIN_SUBMIT_INTERVAL_MS ?? "0");
const lastSubmitTimes = new Map<string, number>();

let relayWs: WebSocket | null = null;
let relayWorkerKey: string | null = null;

let engineWs: WebSocket | null = null;
let engineReady = false;
let engineConnecting = false;

let relayHeartbeatTimer: ReturnType<typeof setInterval> | null = null;

type PendingJob = {
  agent_id: string;
  agent_name?: string;
  instruction: string;
  service_id: string;
  frame_ids: string[];
  created_at?: string;
};

const pendingJobs = new Map<string, PendingJob>();

const inFlight = new Set<string>();
const inFlightPayloads = new Map<string, any>();
const inFlightSentAt = new Map<string, number>();
const jobRetries = new Map<string, number>();
const jobQueue: Array<{ job_id: string; payload: any }> = [];
const workerUnavailableUntil = new Map<string, number>();
const latestBatchSeqByAgent = new Map<string, number>();
let batchSeqCounter = 0;

function log(...args: any[]) {
  console.log("[relay-engine-adapter]", ...args);
}
function debug(...args: any[]) {
  if (ADAPTER_VERBOSE) log(...args);
}
function warn(...args: any[]) {
  console.warn("[relay-engine-adapter]", ...args);
}
function err(...args: any[]) {
  console.error("[relay-engine-adapter]", ...args);
}

function uuid() {
  return crypto.randomUUID();
}

function toBool(value: any, fallback = false): boolean {
  if (typeof value === "boolean") return value;
  if (typeof value === "number") return value !== 0;
  if (typeof value === "string") {
    const v = value.trim().toLowerCase();
    if (!v) return fallback;
    return v === "1" || v === "true" || v === "yes" || v === "on";
  }
  return fallback;
}

function normalizeAgent(a: any): RelayAgent | null {
  const id = String(a?.id ?? a?.agent_id ?? a?.agentId ?? a?.agentID ?? "");
  if (!id) return null;

  const name = String(a?.name ?? a?.agent_name ?? a?.agentName ?? "");
  const instruction = String(a?.instruction ?? a?.prompt ?? a?.text ?? "").trim();
  const rawCustom = a?.custom_yolo;
  let custom_yolo: RelayAgentCustomYOLO | undefined;
  if (rawCustom && typeof rawCustom === "object") {
    custom_yolo = {
      enabled: toBool(rawCustom.enabled, false),
      model_rel_path: typeof rawCustom.model_rel_path === "string" ? rawCustom.model_rel_path : undefined,
      model_filename: typeof rawCustom.model_filename === "string" ? rawCustom.model_filename : undefined,
      model_size_bytes: Number.isFinite(Number(rawCustom.model_size_bytes)) ? Number(rawCustom.model_size_bytes) : undefined,
      runtime_defaults_enable_vlm: rawCustom.runtime_defaults_enable_vlm != null ? toBool(rawCustom.runtime_defaults_enable_vlm, false) : undefined,
      runtime_defaults_enable_face: rawCustom.runtime_defaults_enable_face != null ? toBool(rawCustom.runtime_defaults_enable_face, false) : undefined,
      runtime_desired_replicas: Number.isFinite(Number(rawCustom.runtime_desired_replicas)) ? Number(rawCustom.runtime_desired_replicas) : undefined,
    };
  }

  return { id, name, instruction, custom_yolo };
}

async function fetchFrameBytes(frameId: string): Promise<Uint8Array> {
  if (!relayWorkerKey) throw new Error("Relay worker key not set");
  const url = `${RELAY_HTTP_BASE}/worker/frames/${encodeURIComponent(frameId)}`;
  const res = await fetch(url, { headers: { "X-Worker-Key": relayWorkerKey } });
  if (!res.ok) throw new Error(`Failed to fetch frame ${frameId}: ${res.status} ${res.statusText}`);
  return new Uint8Array(await res.arrayBuffer());
}

async function uploadSnapshot(snapshotPath: string): Promise<string | null> {
  try {
    // Read snapshot file
    const snapshotData = await readFile(snapshotPath);

    // Upload to relay
    const url = `${RELAY_HTTP_BASE}/cv/upload-snapshot`;
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'image/jpeg' },
      body: snapshotData
    });

    if (!res.ok) {
      err(`Failed to upload snapshot: ${res.status} ${res.statusText}`);
      return null;
    }

    const result = await res.json() as { snapshot_path?: string };
    return result.snapshot_path || null;
  } catch (e) {
    err("Snapshot upload error:", e);
    return null;
  }
}

function requeueInFlightJobs() {
  if (inFlightPayloads.size === 0) return;

  const toRetry: Array<{ job_id: string; payload: any }> = [];
  for (const [job_id, payload] of inFlightPayloads.entries()) {
    toRetry.push({ job_id, payload });
    inFlight.delete(job_id);
    inFlightSentAt.delete(job_id);
  }
  inFlightPayloads.clear();

  while (toRetry.length) jobQueue.unshift(toRetry.pop()!);
  trimQueueToLimit(MAX_QUEUE_SIZE);
  log("re-queued in-flight jobs after engine disconnect", { count: jobQueue.length });
}

function dropQueuedJobsForService(serviceID: string, agentID?: string) {
  let dropped = 0;
  for (let i = jobQueue.length - 1; i >= 0; i--) {
    const entry = jobQueue[i];
    if (!entry) continue;
    const qjId = entry.job_id;
    const ctx = pendingJobs.get(qjId);
    if (
      ctx?.service_id === serviceID &&
      (!agentID || ctx?.agent_id === agentID) &&
      !inFlight.has(qjId)
    ) {
      jobQueue.splice(i, 1);
      pendingJobs.delete(qjId);
      jobRetries.delete(qjId);
      dropped++;
    }
  }
  return dropped;
}

function batchAgentKey(serviceID: string, agentID: string) {
  return `${serviceID}::${agentID}`;
}

function servicePressure(serviceID: string) {
  let queued = 0;
  let inFlightByService = 0;
  for (const entry of jobQueue) {
    const ctx = pendingJobs.get(entry.job_id);
    if (ctx?.service_id === serviceID) queued++;
  }
  for (const job_id of inFlight) {
    const ctx = pendingJobs.get(job_id);
    if (ctx?.service_id === serviceID) inFlightByService++;
  }
  return {
    queued,
    in_flight: inFlightByService,
    total: queued + inFlightByService,
  };
}

function trimQueueToLimit(limit: number) {
  while (jobQueue.length > limit) {
    const dropped = jobQueue.shift();
    if (!dropped) break;
    pendingJobs.delete(dropped.job_id);
    inFlightPayloads.delete(dropped.job_id);
    inFlightSentAt.delete(dropped.job_id);
    jobRetries.delete(dropped.job_id);
  }
}

function checkInFlightTimeouts() {
  if (inFlight.size === 0) return;
  const now = Date.now();
  let recovered = 0;

  for (const job_id of Array.from(inFlight)) {
    const sentAt = inFlightSentAt.get(job_id) ?? now;
    if (now-sentAt < IN_FLIGHT_TIMEOUT_MS) continue;

    const payload = inFlightPayloads.get(job_id);
    const ctx = pendingJobs.get(job_id);

    inFlight.delete(job_id);
    inFlightPayloads.delete(job_id);
    inFlightSentAt.delete(job_id);

    const retries = (jobRetries.get(job_id) ?? 0) + 1;
    if (payload && ctx && retries <= MAX_JOB_RETRIES) {
      jobRetries.set(job_id, retries);
      jobQueue.unshift({ job_id, payload });
      recovered++;
      warn("Timed-out in-flight job requeued", {
        job_id,
        retries,
        service_id: ctx.service_id,
        queue: jobQueue.length,
      });
      continue;
    }

    pendingJobs.delete(job_id);
    jobRetries.delete(job_id);
    recovered++;
    warn("Timed-out in-flight job dropped", {
      job_id,
      service_id: ctx?.service_id,
      retries: retries - 1,
    });
  }

  if (recovered > 0) {
    trimQueueToLimit(MAX_QUEUE_SIZE);
    pumpQueue();
  }
}

function ensureEngineConnection() {
  if (engineWs && (engineWs.readyState === WebSocket.OPEN || engineWs.readyState === WebSocket.CONNECTING)) return;
  if (engineConnecting) return;

  engineReady = false;
  engineConnecting = true;

  engineWs = new WebSocket(ENGINE_WS_URL);
  (engineWs as any).binaryType = "arraybuffer";

  engineWs.onopen = () => {
    engineConnecting = false;
    log("Connected to engine", ENGINE_WS_URL);

    const reg: any = { type: "i_am_server", version: ENGINE_VERSION };
    if (ENGINE_TOKEN) reg.token = ENGINE_TOKEN;

    try {
      engineWs!.send(encode(reg));
    } catch (e) {
      err("Failed to send i_am_server", e);
      return;
    }

    engineReady = true;
    log("Engine ready (assumed on open)");
    pumpQueue();
  };

  engineWs.onmessage = (ev) => {
    try {
      const bin =
        ev.data instanceof ArrayBuffer
          ? new Uint8Array(ev.data)
          : ev.data instanceof Uint8Array
            ? ev.data
            : typeof Buffer !== "undefined" && Buffer.isBuffer(ev.data)
              ? ev.data
              : null;

      if (!bin) {
        warn("Engine message is not binary; ignoring", { type: typeof ev.data });
        return;
      }

      const decoded = decode(bin) as any;

      if (decoded?.type === "server_welcome") {
        engineReady = true;
        log("Engine ready (server_welcome)");
        pumpQueue();
        return;
      }

      if (decoded?.type === "worker_response") {
        handleEngineWorkerResponse(decoded as EngineToServer);
        return;
      }

      if (decoded?.type === "error") {
        err("Engine error:", decoded);
        return;
      }

      if (!engineReady) {
        engineReady = true;
        pumpQueue();
      }
    } catch (e) {
      err("Failed to decode engine message", e);
    }
  };

  engineWs.onerror = (e) => {
    err("Engine websocket error", e);
  };

  engineWs.onclose = (ev: any) => {
    engineReady = false;
    engineConnecting = false;

    requeueInFlightJobs();

    warn("Engine disconnected; reconnecting in 2s", {
      code: ev?.code,
      reason: ev?.reason,
    });

    setTimeout(() => ensureEngineConnection(), 2000);
  };
}

function ensureRelayConnection() {
  if (relayWs && (relayWs.readyState === WebSocket.OPEN || relayWs.readyState === WebSocket.CONNECTING)) return;

  relayWorkerKey = null;

  if (relayHeartbeatTimer) {
    clearInterval(relayHeartbeatTimer);
    relayHeartbeatTimer = null;
  }

  relayWs = new WebSocket(RELAY_WS_URL);

  relayWs.onopen = () => {
    log("Connected to relay", RELAY_WS_URL);
    relayWs!.send(JSON.stringify({ type: "register", data: { worker_id: WORKER_ID } }));
  };

  relayWs.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data) as RelayMessage;

      if (msg.type === "registered") {
        relayWorkerKey = msg.data?.key;
        log("Registered as relay worker", WORKER_ID);

        relayHeartbeatTimer = setInterval(() => {
          try {
            relayWs?.send(JSON.stringify({ type: "heartbeat" }));
          } catch {}
        }, 10_000);

        return;
      }

      if (msg.type === "frame_batch") {
        void handleFrameBatch(msg);
        return;
      }

      if (msg.type === "vlm_trigger") {
        // Only VLM adapters process scene analysis triggers
        if (ENGINE_WORKER_TYPE === "vlm") {
          void handleVLMTrigger(msg);
        }
        return;
      }

      if (msg.type === "face_lookup") {
        // Only face recognition adapters process face lookup requests
        if (ENGINE_WORKER_TYPE === "face_recognition") {
          void handleFaceLookup(msg);
        }
        return;
      }
    } catch (e) {
      err("Failed to parse relay message", e);
    }
  };

  relayWs.onerror = (e) => err("Relay websocket error", e);

  relayWs.onclose = () => {
    relayWorkerKey = null;

    if (relayHeartbeatTimer) {
      clearInterval(relayHeartbeatTimer);
      relayHeartbeatTimer = null;
    }

    warn("Relay disconnected; reconnecting in 2s");
    setTimeout(() => ensureRelayConnection(), 2000);
  };
}

function safeToString(v: any) {
  try {
    return typeof v === "string" ? v : JSON.stringify(v);
  } catch {
    return String(v);
  }
}

function parseVlmOutput(output: any): { data: Record<string, any>; raw?: any } {
  try {
    const unwrap = (o: any) => (o && typeof o === "object" && Array.isArray(o.output) && o.output[0] ? o.output[0] : o);
    const item = unwrap(output);

    if (item && typeof item === "object" && "response" in item) {
      const response = typeof item.response === "string" ? item.response : safeToString(item.response);
      const trimmed = response.trim();

      // Build result data, preserving all fields from item
      const summaryText = typeof item.summary === "string" ? item.summary : response;
      const resultData: Record<string, any> = {
        summary: summaryText,
        brief: summaryText.split(".")[0]?.trim() || summaryText,
        ai_summary: summaryText,
      };

      // Preserve severity and other fields from worker output
      if (item.severity !== undefined) resultData.severity = item.severity;
      for (const key in item) {
        if (key !== "response" && key !== "summary" && item[key] !== undefined) {
          resultData[key] = item[key];
        }
      }

      if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
        try {
          const parsed = JSON.parse(trimmed);
          if (parsed && typeof parsed === "object") {
            return { data: { ...resultData, ...parsed }, raw: response };
          }
        } catch {}
      }

      return { data: resultData, raw: response };
    }

    if (typeof item === "string") return { data: { summary: item, brief: item, ai_summary: item }, raw: item };
    if (item && typeof item === "object") return { data: item as any };
    const fallback = safeToString(item);
    return { data: { summary: fallback, brief: fallback, ai_summary: fallback }, raw: item };
  } catch {
    return { data: { summary: safeToString(output) }, raw: output };
  }
}

type ParsedDetection = {
  label: string;
  confidence: number;
  class_id?: number;
  x_min: number;
  y_min: number;
  x_max: number;
  y_max: number;
  track_id?: number;
  global_person_id?: string;
  person_id?: string;
  person_name?: string;
  match_score?: number;
  identity_source?: string;
  identity_confidence?: number;
};

function toBBoxes(item: any): { bboxes: ParsedDetection[]; extras: Record<string, any>; data: Record<string, any> } {
  const detections = Array.isArray(item?.detections) ? item.detections : [];
  const imageWidth = Number(item?.image_width ?? 0);
  const imageHeight = Number(item?.image_height ?? 0);
  const bboxes = detections
    .map((d: any) => {
      const label = String(d?.label ?? "object");
      const confidence = Number(d?.confidence ?? d?.score ?? 0);
      const box = d?.box || d?.bbox;
      let x_min = Number(box?.x_min);
      let y_min = Number(box?.y_min);
      let x_max = Number(box?.x_max);
      let y_max = Number(box?.y_max);

      // Backward compatibility: some workers only emit pixel-space boxes.
      if (!(Number.isFinite(x_min) && Number.isFinite(y_min) && Number.isFinite(x_max) && Number.isFinite(y_max))) {
        const px = d?.box_px || d?.bbox_px;
        if (px && imageWidth > 0 && imageHeight > 0) {
          x_min = Number(px.x_min) / imageWidth;
          y_min = Number(px.y_min) / imageHeight;
          x_max = Number(px.x_max) / imageWidth;
          y_max = Number(px.y_max) / imageHeight;
        }
      }

      const track_id = d?.track_id != null ? Number(d.track_id) : undefined;
      const class_id = d?.class_id != null ? Number(d.class_id) : undefined;
      const det: ParsedDetection = { label, confidence, class_id, x_min, y_min, x_max, y_max, track_id };
      if (d?.global_person_id) det.global_person_id = String(d.global_person_id);
      if (d?.person_id) det.person_id = String(d.person_id);
      if (d?.person_name) det.person_name = String(d.person_name);
      if (d?.match_score != null) det.match_score = Number(d.match_score);
      if (d?.identity_source) det.identity_source = String(d.identity_source);
      if (d?.identity_confidence != null) det.identity_confidence = Number(d.identity_confidence);
      return det;
    })
    .filter((b: any) =>
      Number.isFinite(b.x_min) &&
      Number.isFinite(b.y_min) &&
      Number.isFinite(b.x_max) &&
      Number.isFinite(b.y_max) &&
      b.x_max > b.x_min &&
      b.y_max > b.y_min
    );

  const byLabel: Record<string, number> = {};
  for (const b of bboxes) byLabel[b.label] = (byLabel[b.label] ?? 0) + 1;

  const summaryParts = Object.entries(byLabel)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 6)
    .map(([k, v]) => `${k}: ${v}`);

  const summary = summaryParts.length ? `Detections — ${summaryParts.join(", ")}` : "No detections";

  const extras: Record<string, any> = {};

  // Copy all fields from item to extras, excluding the ones we already processed
  const excludedKeys = new Set(['id', 'detections', 'image_width', 'image_height']);
  for (const key in item) {
    if (!excludedKeys.has(key) && item[key] !== undefined) {
      extras[key] = item[key];
    }
  }

  // Ensure image dimensions are always included if present
  if (typeof item?.image_width === "number") extras.image_width = item.image_width;
  if (typeof item?.image_height === "number") extras.image_height = item.image_height;

  return {
    data: { summary, detections },
    bboxes,
    extras,
  };
}

/**
 * Supports worker output as:
 * { output: [ {id, detections, image_width, image_height}, ... ] }
 */
function parseObjectDetectionBatch(output: any): Array<{ id: string; data: any; bboxes: ParsedDetection[]; extras: Record<string, any> }> {
  const arr: any[] =
    output && typeof output === "object" && Array.isArray(output.output) ? output.output :
    Array.isArray(output) ? output :
    output ? [output] : [];

  const results: Array<{ id: string; data: any; bboxes: ParsedDetection[]; extras: Record<string, any> }> = [];

  for (const item of arr) {
    // Compatibility with nested shape: {id: "<job>", output: [{id, detections, ...}]}
    if (item && typeof item === "object" && Array.isArray(item.output)) {
      for (const nested of item.output) {
        const id = String(nested?.id ?? item?.id ?? "");
        const parsed = toBBoxes(nested ?? {});
        results.push({ id, data: parsed.data, bboxes: parsed.bboxes, extras: parsed.extras });
      }
      continue;
    }
    const id = String(item?.id ?? "");
    const parsed = toBBoxes(item ?? {});
    results.push({ id, data: parsed.data, bboxes: parsed.bboxes, extras: parsed.extras });
  }

  return results;
}

/**
 * handleVLMTrigger — processes relay-initiated VLM analysis requests.
 *
 * When PPE/person detection finds a violation with snapshot, the relay sends
 * a vlm_trigger message containing a frame_id (the snapshot stored as an in-memory
 * frame) and a context string describing what was detected.  This adapter
 * downloads the frame and submits it to the VLM engine exactly like a normal
 * frame_batch job, so the existing result-handling path (parseVlmOutput +
 * handleEngineWorkerResponse) works unchanged.
 */
async function handleVLMTrigger(msg: RelayMessage) {
  if (!relayWorkerKey) {
    warn("vlm_trigger: ignoring — relay worker key not set yet");
    return;
  }

  const d = msg.data ?? {};
  const agent_id  = String(d.agent_id  ?? "");
  const service_id = String(d.service_id ?? "");
  const frame_id  = String(d.frame_id  ?? "");
  const context   = String(d.context   ?? "");

  if (!agent_id || !service_id || !frame_id) {
    warn("vlm_trigger: missing required fields", d);
    return;
  }

  // Back-off if the VLM engine is currently saturated
  if (inFlight.size >= MAX_IN_FLIGHT && jobQueue.length >= MAX_QUEUE_SIZE) {
    debug("vlm_trigger: dropping — queue saturated", { service_id, in_flight: inFlight.size });
    return;
  }

  // Download the snapshot frame (stored as a temporary in-memory frame by relay)
  let frameBytes: Uint8Array;
  try {
    frameBytes = await fetchFrameBytes(frame_id);
  } catch (e) {
    err("vlm_trigger: failed to fetch snapshot frame", frame_id, e);
    return;
  }

  const job_id = uuid();
  const resource: Resource = { id: frame_id, type: "image", data: frameBytes };

  const systemText =
    "You are a workplace safety and security monitoring AI. " +
    "Analyze the image carefully and describe what you see. " +
    "Be specific about people, activities, hazards, and safety concerns. " +
    "Always include STATUS:SAFE or STATUS:ALERT, and SEVERITY:LOW|MEDIUM|HIGH. " +
    "Use STATUS:SAFE with SEVERITY:LOW when no visible hazard or incident is present (including normal office activity). " +
    "Use SEVERITY:MEDIUM only for plausible but non-immediate risks. " +
    "Use SEVERITY:HIGH only for clear active danger like fire, violence, serious injury, or medical emergency. " +
    "Keep your response concise (2-4 sentences).";

  const userText = context
    ? `Reported violation context: ${context}\n\nDescribe what you see in this image. Verify if the reported issue is visible and note any other safety concerns.`
    : "Describe what you see in this image. Focus on people, activities, safety equipment usage, and any hazards or violations.";

  const jobInput = {
    inputs: [
      {
        id: frame_id,
        messages: [
          { role: "system", content: [{ type: "text", text: systemText }] },
          {
            role: "user",
            content: [
              { type: "image", image: { __type: "resource-ref", id: frame_id } as any },
              { type: "text", text: userText },
            ],
          },
        ],
      },
    ],
  };

  const worker_request = {
    type: "worker_request",
    jobs: [{ job_id, worker_type: "vlm", input: jobInput }],
    resources: [resource],
  };

  pendingJobs.set(job_id, {
    agent_id,
    agent_name: undefined,
    instruction: context,
    service_id,
    frame_ids: [frame_id],
    created_at: new Date().toISOString(),
  });

  enqueueJob(job_id, worker_request);
  debug("vlm_trigger enqueued", { agent_id, service_id, frame_id, context: context.slice(0, 80) });
}

/**
 * handleFaceLookup — processes relay-initiated face recognition requests.
 *
 * When a detection worker finds a first-seen person, the relay sends a
 * face_lookup message with a frame_id (the frame stored in relay memory)
 * and person bbox.  The face adapter downloads the frame and submits it
 * to the face recognition engine.  Results flow back via the normal event
 * path and the relay's FaceIdentityService caches them.
 */
async function handleFaceLookup(msg: RelayMessage) {
  if (!relayWorkerKey) {
    warn("face_lookup: ignoring — relay worker key not set yet");
    return;
  }

  const d = msg.data ?? {};
  const media_id  = String(d.media_id  ?? "");
  const track_id  = Number(d.track_id  ?? -1);
  const frame_id  = String(d.frame_id  ?? "");
  const person_bbox = d.person_bbox ?? {};

  if (!media_id || !frame_id) {
    warn("face_lookup: missing required fields", d);
    return;
  }

  if (inFlight.size >= MAX_IN_FLIGHT && jobQueue.length >= MAX_QUEUE_SIZE) {
    debug("face_lookup: dropping — queue saturated", { media_id, in_flight: inFlight.size });
    return;
  }

  let frameBytes: Uint8Array;
  try {
    frameBytes = await fetchFrameBytes(frame_id);
  } catch (e) {
    err("face_lookup: failed to fetch frame", frame_id, e);
    return;
  }

  const job_id = uuid();
  const resource: Resource = { id: frame_id, type: "image", data: frameBytes };

  // Build input that worker_face_recognition.py expects: frame with filepath + bbox metadata.
  // The engine saves the resource as a temp file and passes filepath to the worker.
  const jobInput = {
    media_id,
    service_id: media_id,
    inputs: [
      {
        id: frame_id,
        filepath: { __type: "resource-ref", id: frame_id } as ResourceRef,
        media_id,
        _face_lookup: true,
        track_id,
        person_bbox,
      },
    ],
  };

  const worker_request = {
    type: "worker_request",
    jobs: [{ job_id, worker_type: "face_recognition", input: jobInput }],
    resources: [resource],
  };

  // Use a synthetic agent entry so the result routes back through the event bus.
  pendingJobs.set(job_id, {
    agent_id: `face_lookup_${media_id}`,
    agent_name: undefined,
    instruction: `Face lookup: media=${media_id} track=${track_id}`,
    service_id: media_id,
    frame_ids: [frame_id],
    created_at: new Date().toISOString(),
  });

  enqueueJob(job_id, worker_request);
  debug("face_lookup enqueued", { media_id, track_id, frame_id });
}

async function handleFrameBatch(msg: RelayMessage) {
  if (!relayWorkerKey) {
    warn("Ignoring frame_batch: no relay worker key yet");
    return;
  }

  const payload = msg.data ?? {};
  const service_id = String(payload.service_id ?? "");
  const frames: string[] = Array.isArray(payload.frames) ? payload.frames.map(String) : [];

  const agentsRaw = Array.isArray(payload.agents) ? payload.agents : [];
  const agents = agentsRaw.map(normalizeAgent).filter(Boolean) as RelayAgent[];

  if (agentsRaw.length && agents.length === 0) {
    warn("Agents received but none had a usable id. Raw agents:", agentsRaw);
  }

  if (!service_id || frames.length === 0 || agents.length === 0) return;

  const unavailableUntil = workerUnavailableUntil.get(ENGINE_WORKER_TYPE) ?? 0;
  if (Date.now() < unavailableUntil) return;

  const batchMeta = (payload.metadata && typeof payload.metadata === "object") ? payload.metadata : {};
  const procFPS = Number((batchMeta as any).processing_fps ?? 0);
  const vlmFPS = Number((batchMeta as any).vlm_fps ?? 0);
  const serviceName = String((batchMeta as any).service_name ?? "");
  const perCameraIntervalMs = (() => {
    if (IS_BBOX_WORKER && procFPS > 0) return Math.max(0, Math.round(1000 / procFPS));
    if (!IS_BBOX_WORKER && vlmFPS > 0) return Math.max(0, Math.round(1000 / vlmFPS));
    return MIN_SUBMIT_INTERVAL_MS;
  })();
  const pressure = servicePressure(service_id);
  const adaptivePenalty = Math.min(250, pressure.queued * 8 + pressure.in_flight * 12);
  const adaptiveIntervalMs = perCameraIntervalMs + adaptivePenalty;

  // ✅ Per-service throttle: prevent overloading workers and enforce camera FPS targets.
  if (adaptiveIntervalMs > 0) {
    const now = Date.now();
    const last = lastSubmitTimes.get(service_id) ?? 0;
    if (now - last < adaptiveIntervalMs) return;
    lastSubmitTimes.set(service_id, now);
  }

  // Queue eviction is applied per (service, agent) below to keep multi-agent fairness.

  // ✅ Backpressure: skip if all in-flight slots are occupied (fresh results coming soon)
  if (IS_BBOX_WORKER && inFlight.size >= MAX_IN_FLIGHT) {
    debug("Skipping frame_batch — all in-flight slots occupied", { service_id, in_flight: inFlight.size, queued: jobQueue.length });
    return;
  }

  if (!IS_BBOX_WORKER && inFlight.size >= MAX_IN_FLIGHT && jobQueue.length >= MAX_QUEUE_SIZE) {
    debug("Skipping frame_batch — queue saturated", { service_id, in_flight: inFlight.size, queued: jobQueue.length });
    return;
  }

  // Use newest frames first for both bbox and VLM to keep latency bounded.
  const selectedFrames = IS_BBOX_WORKER
    ? frames.slice(-Math.max(1, MAX_FRAMES_PER_JOB))
    : frames.slice(-Math.max(1, MAX_FRAMES_PER_JOB));

  debug("frame_batch received", {
    service_id,
    frames: frames.length,
    selected_frames: selectedFrames.length,
    agents: agents.length,
    service_pressure: pressure,
    worker_id: WORKER_ID,
    engine_worker_type: ENGINE_WORKER_TYPE,
  });

  const batchSeq = ++batchSeqCounter;
  if (IS_BBOX_WORKER && DROP_STALE_BBOX_JOBS) {
    for (const agent of agents) {
      latestBatchSeqByAgent.set(batchAgentKey(service_id, agent.id), batchSeq);
      const dropped = dropQueuedJobsForService(service_id, agent.id);
      if (dropped > 0) {
        debug("Dropped stale queued jobs before frame fetch", {
          service_id,
          agent_id: agent.id,
          dropped,
          remaining: jobQueue.length,
        });
      }
    }
  }

  let frameBytes: Uint8Array[] = [];
  try {
    frameBytes = await Promise.all(selectedFrames.map(fetchFrameBytes));
  } catch (e) {
    err("Failed to fetch frames for batch", e);
    return;
  }

  for (const agent of agents) {
    if (IS_BBOX_WORKER && DROP_STALE_BBOX_JOBS) {
      const key = batchAgentKey(service_id, agent.id);
      const isStaleHandler = latestBatchSeqByAgent.get(key) !== batchSeq;
      if (isStaleHandler && servicePressure(service_id).total > 0) {
        debug("Skipping stale fetched frame_batch", {
          service_id,
          agent_id: agent.id,
          selected_frames: selectedFrames,
        });
        continue;
      }
    }

    if (jobQueue.length > 0) {
      const dropped = dropQueuedJobsForService(service_id, agent.id);
      if (dropped > 0) {
        debug("Dropped stale queued jobs", {
          service_id,
          agent_id: agent.id,
          dropped,
          remaining: jobQueue.length,
        });
      }
    }

    const job_id = uuid();

    const resources: Resource[] = selectedFrames
      .map((frameId, idx) => {
        const bytes = frameBytes[idx];
        if (!bytes) return null;
        return { id: frameId, type: "image" as const, data: bytes };
      })
      .filter(Boolean) as Resource[];

    if (resources.length === 0) continue;

    let jobInput: any;

    // ---------- BBOX WORKERS (object_detection / helmet_detection / ppe_detection) ----------
    if (IS_BBOX_WORKER) {
      // ✅ Send a batch of inputs so worker returns output per frame
      jobInput = {
        media_id: service_id, // Per-stream tracking (ByteTrack uses this to isolate trackers per camera)
        media_name: serviceName || service_id, // Friendly camera name for snapshot labelling
        inputs: resources.map((r) => ({
          id: r.id,
          filepath: { __type: "resource-ref", id: r.id } as ResourceRef,
        })),
      };

      if (IS_CUSTOM_DETECTION_WORKER) {
        const customOpts = resolveCustomJobOptions(agent.custom_yolo, {
          customModelPath: process.env.CUSTOM_MODEL_PATH,
          customEnableVLM: process.env.CUSTOM_ENABLE_VLM,
          customEnableFace: process.env.CUSTOM_ENABLE_FACE,
        });
        jobInput.custom_yolo_enabled = customOpts.custom_yolo_enabled;
        jobInput.custom_model_path = customOpts.custom_model_path;
        jobInput.custom_enable_vlm = customOpts.custom_enable_vlm;
        jobInput.custom_enable_face = customOpts.custom_enable_face;
      }
    }
    // ---------- VLM / OTHER ----------
    else {
      const imageContent = resources.map((r) => ({
        type: "image",
        image: { __type: "resource-ref", id: r.id } as ResourceRef,
      }));

      const systemText =
        "You are a workplace safety and security monitoring AI. " +
        "Analyze images carefully and describe what you see. " +
        "Be specific about people, activities, hazards, and safety concerns. " +
        "Always include STATUS:SAFE or STATUS:ALERT, and SEVERITY:LOW|MEDIUM|HIGH. " +
        "Use STATUS:SAFE with SEVERITY:LOW when no visible hazard or incident is present (including normal office activity). " +
        "Use SEVERITY:MEDIUM only for plausible but non-immediate risks. " +
        "Use SEVERITY:HIGH only for clear active danger like fire, violence, serious injury, or medical emergency. " +
        "Keep your response concise (2-4 sentences).";
      const userText =
        `${agent.instruction}\n\n` +
        "Describe what you see in this image. Focus on people, their activities, any safety hazards, and anything unusual or concerning. Be specific.";

      jobInput = {
        inputs: [
          {
            id: resources[0]!.id, // Safe: resources.length checked above
            messages: [
              { role: "system", content: [{ type: "text", text: systemText }] },
              { role: "user", content: [...imageContent, { type: "text", text: userText }] },
            ],
          },
        ],
      };
    }

    const worker_request = {
      type: "worker_request",
      jobs: [
        {
          job_id,
          worker_type: ENGINE_WORKER_TYPE,
          input: jobInput,
        },
      ],
      resources,
    };

    pendingJobs.set(job_id, {
      agent_id: agent.id,
      agent_name: agent.name,
      instruction: agent.instruction,
      service_id,
      frame_ids: selectedFrames,
      created_at: msg.created_at,
    });

    enqueueJob(job_id, worker_request);
  }
}

function enqueueJob(job_id: string, payload: any) {
  if (jobQueue.length >= MAX_QUEUE_SIZE) {
    trimQueueToLimit(MAX_QUEUE_SIZE - 1);
  }
  jobQueue.push({ job_id, payload });
  trimQueueToLimit(MAX_QUEUE_SIZE);
  debug("job queued", { job_id, queue: jobQueue.length, in_flight: inFlight.size });
  pumpQueue();
}

function pumpQueue() {
  ensureEngineConnection();
  if (!engineWs || engineWs.readyState !== WebSocket.OPEN) return;
  if (!engineReady) return;

  while (inFlight.size < MAX_IN_FLIGHT && jobQueue.length > 0) {
    const next = jobQueue.shift()!;
    inFlight.add(next.job_id);
    inFlightPayloads.set(next.job_id, next.payload);
    inFlightSentAt.set(next.job_id, Date.now());

    try {
      debug("dispatching job to engine", { job_id: next.job_id, queue: jobQueue.length, in_flight: inFlight.size });
      engineWs.send(encode(next.payload));
    } catch (e) {
      err("Failed to send job to engine", e);
      inFlight.delete(next.job_id);
      inFlightPayloads.delete(next.job_id);
      inFlightSentAt.delete(next.job_id);
    }
  }
}

async function handleEngineWorkerResponse(msg: EngineToServer) {
  const job_id = msg.job_id;

  // Extract frame bytes before clearing in-flight state for optional snapshot upload.
  const storedPayload = inFlightPayloads.get(job_id);
  const storedResources: Resource[] = storedPayload?.resources ?? [];

  inFlight.delete(job_id);
  inFlightPayloads.delete(job_id);
  inFlightSentAt.delete(job_id);
  jobRetries.delete(job_id);

  const ctx = pendingJobs.get(job_id);
  pendingJobs.delete(job_id);

  if (!ctx) {
    pumpQueue();
    return;
  }

  try {
    // Route all bbox workers (object_detection / helmet_detection / ppe_detection) through detection parsing
    if (IS_BBOX_WORKER) {
      const parsedBatch = parseObjectDetectionBatch(msg.output);
      let sentCount = 0;

      // ✅ Emit one relay event PER FRAME => smooth overlay, correct per-camera filtering
      for (const item of parsedBatch) {
        const itemError = typeof item.extras?.error === "string" ? item.extras.error : "";
        if (itemError.includes("No worker available for worker_type")) {
          workerUnavailableUntil.set(ENGINE_WORKER_TYPE, Date.now() + WORKER_UNAVAILABLE_BACKOFF_MS);
          continue
        }
        if (itemError.includes("Dropped stale job before inference") || item.extras?.stale_dropped === true) {
          continue;
        }

        const frameId = item.id || ctx.frame_ids?.[ctx.frame_ids.length - 1];

        const metadata: Record<string, any> = {
          service_id: ctx.service_id,
          service_ids: [ctx.service_id], // ✅ Multi-camera segregation
          frame_ids: ctx.frame_ids,
          frame_id: frameId,
          engine_job_id: job_id,
          worker_id: WORKER_ID,
          engine_worker_type: ENGINE_WORKER_TYPE,
          created_at: new Date().toISOString(),
          ...item.extras,
        };
        const pressureNow = servicePressure(ctx.service_id);
        metadata.queue_pressure = pressureNow.total;
        metadata.queue_pressure_queued = pressureNow.queued;
        metadata.queue_pressure_in_flight = pressureNow.in_flight;

        // Set agent_type for UI filtering.
        metadata.agent_type = deriveAgentType(ENGINE_WORKER_TYPE, IS_CUSTOM_DETECTION_WORKER);
        if (ENGINE_WORKER_TYPE === "face_recognition") {
          metadata.agent_type = "face";
          // Collect unique person names from bboxes for Moments display
          if (item.bboxes?.length) {
            const matched = item.bboxes.filter((b: any) => b.person_name).map((b: any) => b.person_name as string);
            const unique = [...new Set(matched)];
            if (unique.length) {
              metadata.person_names = unique;
              (metadata as any).matched_count = unique.length;
            }
          }
        }

        if (item.bboxes && item.bboxes.length) metadata.bboxes = item.bboxes;

        // Handle snapshot upload if worker provided snapshot (helmet/security violations)
        if (item.extras?.snapshot_path) {
          const uploadedPath = await uploadSnapshot(item.extras.snapshot_path);
          if (uploadedPath) {
            metadata.snapshot_path = uploadedPath;
            debug(`Uploaded snapshot: ${uploadedPath}`);
          }
        }

        // Include VLM summary and violation data in both data and metadata
        const eventData = { ...item.data };
        applyDetectionExtras(item.extras, eventData, metadata);
        attachIdentityMetadataFromBBoxes(item.bboxes, metadata);

        // first_detection flag — already in metadata via ...item.extras spread
        // person_names — collect from bboxes if worker didn't set them in extras
        if (ENGINE_WORKER_TYPE === "ppe_detection" && item.bboxes?.length) {
          const matched = item.bboxes
            .filter((b: any) => b.person_name)
            .map((b: any) => b.person_name as string);
          const unique = [...new Set(matched)];
          if (unique.length && !metadata.person_names?.length) {
            metadata.person_names = unique;
          }
        }

        relayWs?.send(
          JSON.stringify({
            type: "event",
            data: {
              agent_id: ctx.agent_id,
              service_id: ctx.service_id,
              service_ids: [ctx.service_id],
              frame_id: frameId,
              data: eventData,
              metadata,
            },
          })
        );
        sentCount++;
      }

      if (sentCount > 0) {
        workerUnavailableUntil.delete(ENGINE_WORKER_TYPE);
      }
      debug(`engine worker_response (${ENGINE_WORKER_TYPE} batch)`, {
        job_id,
        agent_id: ctx.agent_id,
        frames: sentCount,
      });

      pumpQueue();
      return;
    }

    // ---------- VLM / OTHER ----------
    const parsed = parseVlmOutput(msg.output);
    const data = parsed.data;
    const raw = parsed.raw;
    const dataError = typeof data?.error === "string" ? data.error : "";

    // Internal scheduling/availability failures should not surface as user-facing incidents.
    if (dataError.includes("No worker available for worker_type")) {
      workerUnavailableUntil.set(ENGINE_WORKER_TYPE, Date.now() + WORKER_UNAVAILABLE_BACKOFF_MS);
      warn("Skipping VLM event due worker unavailability", {
        engine_worker_type: ENGINE_WORKER_TYPE,
        error: dataError,
      });
      pumpQueue();
      return;
    }
    if (dataError.includes("Dropped stale job before inference") || data?.stale_dropped === true) {
      pumpQueue();
      return;
    }

    const metadata: Record<string, any> = {
      service_id: ctx.service_id,
      service_ids: [ctx.service_id], // ✅ keep consistent routing behavior
      frame_ids: ctx.frame_ids,
      engine_job_id: job_id,
      worker_id: WORKER_ID,
      engine_worker_type: ENGINE_WORKER_TYPE,
      created_at: new Date().toISOString(),
    };
    const pressureNow = servicePressure(ctx.service_id);
    metadata.queue_pressure = pressureNow.total;
    metadata.queue_pressure_queued = pressureNow.queued;
    metadata.queue_pressure_in_flight = pressureNow.in_flight;

    if (raw !== undefined) metadata.raw = raw;

    // Handle severity-based snapshot for Wellness Check.
    // Uses frame bytes cached in storedResources to avoid extra network fetch.
    const severity = data.severity || "low";
    if (severity === "medium" || severity === "high") {
      const frameData = storedResources[0]?.data;
      if (frameData && frameData.length > 0) {
        try {
          const body = frameData.buffer.slice(
            frameData.byteOffset,
            frameData.byteOffset + frameData.byteLength,
          ) as ArrayBuffer;
          const url = `${RELAY_HTTP_BASE}/cv/upload-snapshot`;
          const res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'image/jpeg' },
            body,
          });
          if (res.ok) {
            const result = await res.json() as { snapshot_path?: string };
            if (result.snapshot_path) {
              metadata.snapshot_path = result.snapshot_path;
              debug(`Wellness snapshot saved (${severity}): ${result.snapshot_path}`);
            }
          }
        } catch (e) {
          err("Failed to save wellness snapshot:", e);
        }
      }
    }

    // Include severity in metadata for UI filtering
    if (severity) {
      metadata.severity = severity;
      metadata.agent_type = "situational";
    }
    if (!data.ai_summary && typeof data.summary === "string") data.ai_summary = data.summary;
    if (!data.brief && typeof data.summary === "string") data.brief = data.summary.split(".")[0] || data.summary;

    const summaryText =
      (typeof data.ai_summary === "string" && data.ai_summary.trim()) ||
      (typeof data.summary === "string" && data.summary.trim()) ||
      (typeof data.brief === "string" && data.brief.trim()) ||
      "";
    const hasSnapshot = typeof metadata.snapshot_path === "string" && metadata.snapshot_path.trim() !== "";
    const hasMeaningfulPayload = summaryText !== "" || hasSnapshot || severity === "medium" || severity === "high";
    if (!hasMeaningfulPayload) {
      debug("Skipping empty low-severity VLM event", {
        service_id: ctx.service_id,
        engine_job_id: job_id,
      });
      pumpQueue();
      return;
    }

    relayWs?.send(
      JSON.stringify({
        type: "event",
        data: {
          agent_id: ctx.agent_id,
          service_id: ctx.service_id,
          service_ids: [ctx.service_id],
          frame_id: ctx.frame_ids?.[ctx.frame_ids.length - 1],
          data,
          metadata,
        },
      })
    );

    debug("engine worker_response (vlm)", { job_id, agent_id: ctx.agent_id });
  } catch (e) {
    err("Failed to handle worker_response", e);
  }

  pumpQueue();
}

// --- boot ---
ensureRelayConnection();
ensureEngineConnection();
const inFlightWatchdog = setInterval(checkInFlightTimeouts, 1000);
if (typeof (inFlightWatchdog as any).unref === "function") {
  (inFlightWatchdog as any).unref();
}

log("Adapter started", {
  RELAY_WS_URL,
  RELAY_HTTP_BASE,
  ENGINE_WS_URL,
  WORKER_ID,
  ENGINE_WORKER_TYPE,
  MAX_FRAMES_PER_JOB,
  MAX_IN_FLIGHT,
  MAX_QUEUE_SIZE,
  IN_FLIGHT_TIMEOUT_MS,
  MAX_JOB_RETRIES,
  WORKER_UNAVAILABLE_BACKOFF_MS,
  ADAPTER_VERBOSE,
});
