export type RelayAgentCustomYOLO = {
  enabled: boolean;
  model_rel_path?: string;
  model_filename?: string;
  model_size_bytes?: number;
  runtime_defaults_enable_vlm?: boolean;
  runtime_defaults_enable_face?: boolean;
  runtime_desired_replicas?: number;
};

export type CustomJobOptions = {
  custom_yolo_enabled: true;
  custom_model_path: string;
  custom_enable_vlm: boolean;
  custom_enable_face: boolean;
};

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

export function resolveCustomJobOptions(
  custom: RelayAgentCustomYOLO | undefined,
  env: {
    customModelPath?: string;
    customEnableVLM?: string | null;
    customEnableFace?: string | null;
  },
): CustomJobOptions {
  const envModelPath = String(env.customModelPath ?? "").trim();
  const modelPath = envModelPath || String(custom?.model_rel_path ?? "").trim();
  const customEnableVLM = env.customEnableVLM != null
    ? toBool(env.customEnableVLM, false)
    : toBool(custom?.runtime_defaults_enable_vlm, false);
  const customEnableFace = env.customEnableFace != null
    ? toBool(env.customEnableFace, false)
    : toBool(custom?.runtime_defaults_enable_face, false);

  return {
    custom_yolo_enabled: true,
    custom_model_path: modelPath,
    custom_enable_vlm: customEnableVLM,
    custom_enable_face: customEnableFace,
  };
}

export function deriveAgentType(engineWorkerType: string, isCustomDetectionWorker: boolean): string {
  if (engineWorkerType === "ppe_detection") return "ppe";
  if (engineWorkerType === "object_detection") return isCustomDetectionWorker ? "custom" : "person";
  if (engineWorkerType === "face_recognition") return "face";
  return "situational";
}

export function applyDetectionExtras(
  extras: Record<string, any> | undefined,
  eventData: Record<string, any>,
  metadata: Record<string, any>,
): void {
  if (!extras) return;

  if (extras.vlm_summary) {
    metadata.vlm_summary = extras.vlm_summary;
    eventData.vlm_summary = extras.vlm_summary;
    eventData.ai_summary = extras.vlm_summary;
    eventData.brief = String(extras.vlm_summary).split(".")[0] || extras.vlm_summary;
  }
  if (extras.violation_count != null) {
    metadata.violation_count = extras.violation_count;
    eventData.violation_count = extras.violation_count;
  }
  if (extras.violation_labels != null) {
    metadata.violation_labels = extras.violation_labels;
    eventData.violation_labels = extras.violation_labels;
  }
  if (extras.person_count != null) {
    eventData.person_count = extras.person_count;
  }
  if (extras.has_violations != null) {
    metadata.has_violations = extras.has_violations;
    eventData.has_violations = extras.has_violations;
  }
  if (extras.event_kind != null) {
    metadata.event_kind = extras.event_kind;
    eventData.event_kind = extras.event_kind;
  }
  if (extras.camera_name != null) metadata.camera_name = extras.camera_name;
  if (extras.camera_timestamp != null) metadata.camera_timestamp = extras.camera_timestamp;
  if (extras.helmet_count != null) {
    metadata.helmet_count = extras.helmet_count;
    eventData.helmet_count = extras.helmet_count;
  }
  if (extras.total_detections != null) {
    metadata.total_detections = extras.total_detections;
    eventData.total_detections = extras.total_detections;
  }
  if (extras.severity != null) {
    metadata.severity = extras.severity;
    eventData.severity = extras.severity;
  }
  if (extras.fall_detected != null) {
    metadata.fall_detected = extras.fall_detected;
    eventData.fall_detected = extras.fall_detected;
  }
  if (extras.vlm_track_ids != null) {
    metadata.vlm_track_ids = extras.vlm_track_ids;
  }

  if (extras.event_brief) {
    const briefText = String(extras.event_brief);
    if (!eventData.brief) eventData.brief = briefText;
    metadata.brief = briefText;
    if (!eventData.ai_summary) eventData.ai_summary = briefText;
  }
}

export function attachIdentityMetadataFromBBoxes(
  bboxes: any[] | undefined,
  metadata: Record<string, any>,
): void {
  if (!Array.isArray(bboxes) || bboxes.length === 0) return;

  const unknownSources = new Set(["", "unknown_track", "reid_new", "reid_match"]);
  const canonicalGlobalID = (b: any): string => {
    const personID = typeof b?.person_id === "string" ? b.person_id.trim() : "";
    const globalID = typeof b?.global_person_id === "string" ? b.global_person_id.trim() : "";
    if (personID && (!globalID || globalID.startsWith("unknown:"))) {
      return `person:${personID}`;
    }
    return globalID;
  };
  const canonicalIdentitySource = (b: any): string => {
    const source = typeof b?.identity_source === "string" ? b.identity_source.trim() : "";
    const personID = typeof b?.person_id === "string" ? b.person_id.trim() : "";
    const globalID = canonicalGlobalID(b);
    if ((personID || globalID.startsWith("person:")) && unknownSources.has(source.toLowerCase())) {
      return "face_recognition";
    }
    return source;
  };
  const knownRank = (b: any): number => {
    const personID = typeof b?.person_id === "string" ? b.person_id.trim() : "";
    if (personID) return 3;
    const globalID = canonicalGlobalID(b);
    if (globalID.startsWith("person:")) return 2;
    const personName = typeof b?.person_name === "string" ? b.person_name.trim() : "";
    if (personName && !personName.toLowerCase().startsWith("unknown")) return 1;
    return 0;
  };
  const rankedBBoxes = [...bboxes].sort((a, b) => knownRank(b) - knownRank(a));

  const globalIDs = [...new Set(bboxes
    .map(canonicalGlobalID)
    .filter((v: any) => typeof v === "string" && v.length > 0))];
  if (globalIDs.length) {
    metadata.global_person_ids = globalIDs;
    if (!metadata.global_person_id) {
      const primaryGlobalID = rankedBBoxes
        .map(canonicalGlobalID)
        .find((v: any) => typeof v === "string" && v.length > 0);
      metadata.global_person_id = primaryGlobalID || globalIDs[0];
    }
  }
  const personIDs = [...new Set(rankedBBoxes
    .map((b: any) => b.person_id)
    .filter((v: any) => typeof v === "string" && v.length > 0))];
  if (personIDs.length && !metadata.person_id) metadata.person_id = personIDs[0];
  const personNames = [...new Set(rankedBBoxes
    .map((b: any) => b.person_name)
    .filter((v: any) => typeof v === "string" && v.length > 0))];
  if (personNames.length && !metadata.person_name) metadata.person_name = personNames[0];
  const identitySources = [...new Set(rankedBBoxes
    .map(canonicalIdentitySource)
    .filter((v: any) => typeof v === "string" && v.length > 0))];
  if (identitySources.length && !metadata.identity_source) metadata.identity_source = identitySources[0];
  const identityConf = rankedBBoxes
    .map((b: any) => {
      if (typeof b?.identity_confidence === "number") return b.identity_confidence;
      if (typeof b?.match_score === "number") return b.match_score;
      return undefined;
    })
    .filter((v: any) => typeof v === "number" && Number.isFinite(v)) as number[];
  if (identityConf.length && metadata.identity_confidence == null) {
    metadata.identity_confidence = Math.max(...identityConf);
  }
}
