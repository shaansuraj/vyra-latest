import { describe, expect, test } from "bun:test";
import {
  applyDetectionExtras,
  attachIdentityMetadataFromBBoxes,
  deriveAgentType,
  resolveCustomJobOptions,
  type RelayAgentCustomYOLO,
} from "./relay_worker_adapter_logic";

describe("relay_worker_adapter_logic", () => {
  test("resolveCustomJobOptions uses env overrides first", () => {
    const custom: RelayAgentCustomYOLO = {
      enabled: true,
      model_rel_path: "/models/agent-a.pt",
      runtime_defaults_enable_vlm: false,
      runtime_defaults_enable_face: false,
    };
    const out = resolveCustomJobOptions(custom, {
      customModelPath: "/env/model.pt",
      customEnableVLM: "1",
      customEnableFace: "true",
    });
    expect(out.custom_yolo_enabled).toBe(true);
    expect(out.custom_model_path).toBe("/env/model.pt");
    expect(out.custom_enable_vlm).toBe(true);
    expect(out.custom_enable_face).toBe(true);
  });

  test("resolveCustomJobOptions falls back to agent defaults", () => {
    const custom: RelayAgentCustomYOLO = {
      enabled: true,
      model_rel_path: "/models/agent-b.pt",
      runtime_defaults_enable_vlm: true,
      runtime_defaults_enable_face: false,
    };
    const out = resolveCustomJobOptions(custom, {});
    expect(out.custom_model_path).toBe("/models/agent-b.pt");
    expect(out.custom_enable_vlm).toBe(true);
    expect(out.custom_enable_face).toBe(false);
  });

  test("resolveCustomJobOptions supports numeric/string env booleans", () => {
    const custom: RelayAgentCustomYOLO = {
      enabled: true,
      model_rel_path: "/models/agent-c.pt",
      runtime_defaults_enable_vlm: false,
      runtime_defaults_enable_face: true,
    };
    const out = resolveCustomJobOptions(custom, {
      customEnableVLM: "0",
      customEnableFace: "yes",
    });
    expect(out.custom_model_path).toBe("/models/agent-c.pt");
    expect(out.custom_enable_vlm).toBe(false);
    expect(out.custom_enable_face).toBe(true);
  });

  test("applyDetectionExtras keeps zero and false values (null-check semantics)", () => {
    const metadata: Record<string, any> = {};
    const data: Record<string, any> = {};
    applyDetectionExtras(
      {
        violation_count: 0,
        person_count: 0,
        has_violations: false,
        event_kind: "person_activity",
      },
      data,
      metadata,
    );
    expect(metadata.violation_count).toBe(0);
    expect(data.person_count).toBe(0);
    expect(metadata.has_violations).toBe(false);
    expect(data.has_violations).toBe(false);
    expect(metadata.event_kind).toBe("person_activity");
  });

  test("applyDetectionExtras sets event_brief and preserves existing brief", () => {
    const metadata: Record<string, any> = {};
    const data: Record<string, any> = { brief: "existing brief" };
    applyDetectionExtras(
      {
        event_brief: "new concise brief",
      },
      data,
      metadata,
    );
    expect(data.brief).toBe("existing brief");
    expect(metadata.brief).toBe("new concise brief");
    expect(data.ai_summary).toBe("new concise brief");
  });

  test("attachIdentityMetadataFromBBoxes extracts global identity fields", () => {
    const metadata: Record<string, any> = {};
    attachIdentityMetadataFromBBoxes(
      [
        {
          global_person_id: "unknown:12",
          person_name: "Unknown #12",
          identity_source: "reid_match",
          identity_confidence: 0.71,
        },
        {
          global_person_id: "person:4",
          person_id: "4",
          person_name: "Abhishek",
          identity_source: "face_recognition",
          identity_confidence: 0.97,
        },
      ],
      metadata,
    );
    expect(metadata.global_person_ids).toEqual(["unknown:12", "person:4"]);
    expect(metadata.global_person_id).toBe("person:4");
    expect(metadata.person_id).toBe("4");
    expect(metadata.person_name).toBe("Abhishek");
    expect(metadata.identity_source).toBe("face_recognition");
    expect(metadata.identity_confidence).toBe(0.97);
  });

  test("attachIdentityMetadataFromBBoxes promotes known person id over unknown global id", () => {
    const metadata: Record<string, any> = {};
    attachIdentityMetadataFromBBoxes(
      [
        {
          global_person_id: "unknown:68",
          person_id: "425ca57f-8254-4709-8a5a-70d4ac8a7172",
          person_name: "SubhGlass",
          identity_source: "unknown_track",
          match_score: 0.582,
        },
      ],
      metadata,
    );
    expect(metadata.global_person_ids).toEqual(["person:425ca57f-8254-4709-8a5a-70d4ac8a7172"]);
    expect(metadata.global_person_id).toBe("person:425ca57f-8254-4709-8a5a-70d4ac8a7172");
    expect(metadata.person_id).toBe("425ca57f-8254-4709-8a5a-70d4ac8a7172");
    expect(metadata.person_name).toBe("SubhGlass");
    expect(metadata.identity_source).toBe("face_recognition");
    expect(metadata.identity_confidence).toBe(0.582);
  });

  test("attachIdentityMetadataFromBBoxes does not override existing metadata values", () => {
    const metadata: Record<string, any> = {
      global_person_id: "person:100",
      person_id: "100",
      person_name: "Existing Name",
      identity_source: "manual",
      identity_confidence: 0.99,
    };
    attachIdentityMetadataFromBBoxes(
      [
        {
          global_person_id: "person:4",
          person_id: "4",
          person_name: "Abhishek",
          identity_source: "face_recognition",
          identity_confidence: 0.97,
        },
      ],
      metadata,
    );
    expect(metadata.global_person_id).toBe("person:100");
    expect(metadata.person_id).toBe("100");
    expect(metadata.person_name).toBe("Existing Name");
    expect(metadata.identity_source).toBe("manual");
    expect(metadata.identity_confidence).toBe(0.99);
  });

  test("deriveAgentType marks custom object detection as custom", () => {
    expect(deriveAgentType("object_detection", true)).toBe("custom");
    expect(deriveAgentType("object_detection", false)).toBe("person");
    expect(deriveAgentType("ppe_detection", false)).toBe("ppe");
    expect(deriveAgentType("face_recognition", false)).toBe("face");
    expect(deriveAgentType("vlm", false)).toBe("situational");
  });
});
