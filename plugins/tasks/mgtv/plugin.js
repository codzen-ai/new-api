// MaaS Seedance video models, keyed by public model name. Callers never see
// the MaaS model name each one names under maasModel, so MaaS channels carry
// no model_mapping (ADR-0002, ADR-0003). Prices are keyed by the public model
// name and read the usage facts tokens, resolution and video_input.
//
// Each endpoint declares:
// - durations: the whole seconds it accepts, and whether it also takes -1
//   (the model picks the length), with the default an omitted duration sends.
// - resolutions: its resolution tiers, in the gateway's spelling.
// - aspectRatios: the aspect ratios it accepts.
// - maxMedia: the most media items of each limited role it takes.
// - outputFormats: the container formats it takes; a model without them
//   refuses output_format.
// An omitted resolution is DEFAULT_RESOLUTION, which every endpoint offers.
// Request validation, the upstream model name and the usage facts are all
// derived from this table.
const ENDPOINTS = {
  "seedance-2.5": {
    maasModel: "msd-2-5-260628",
    durations: { min: 4, max: 30, auto: true, default: -1 },
    resolutions: ["480p", "720p", "1080p"],
    aspectRatios: ["16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"],
    maxMedia: { reference_image: 30, reference_video: 10 },
    outputFormats: ["mp4", "mov"],
  },
  "seedance-2.0-pro": {
    maasModel: "msd-2-0-pro-260128",
    durations: { min: 4, max: 15, auto: false, default: 5 },
    resolutions: ["480p", "720p", "1080p", "4k"],
    aspectRatios: ["16:9", "4:3", "1:1", "3:4", "9:16", "21:9"],
    maxMedia: { reference_image: 9, reference_video: 3 },
  },
  "seedance-2.0-fast": {
    maasModel: "msd-2-0-fast-260128",
    durations: { min: 4, max: 15, auto: false, default: 5 },
    resolutions: ["480p", "720p"],
    aspectRatios: ["16:9", "4:3", "1:1", "3:4", "9:16", "21:9"],
    maxMedia: { reference_image: 9, reference_video: 3 },
  },
  "seedance-2.0-mini": {
    maasModel: "msd-2-0-mini-260615",
    durations: { min: 4, max: 15, auto: false, default: 5 },
    resolutions: ["480p", "720p"],
    aspectRatios: ["16:9", "4:3", "1:1", "3:4", "9:16", "21:9"],
    maxMedia: { reference_image: 9, reference_video: 3 },
  },
};

// The resolution an omitted one sends (spec #48).
const DEFAULT_RESOLUTION = "720p";

// Every resolution tier, in the gateway's spelling, with the spelling MaaS
// takes and the 16:9 max-pixel size the token estimate uses.
const RESOLUTIONS = {
  "480p": { maas: "480p", width: 854, height: 480 },
  "720p": { maas: "720p", width: 1280, height: 720 },
  "1080p": { maas: "1080p", width: 1920, height: 1080 },
  "4k": { maas: "4K", width: 3840, height: 2160 },
};

// Seedance bills tokens = seconds × width × height × 24 / 1024 (the doubao
// plugin's formula). The estimate takes the tier's 16:9 max-pixel size, so it
// covers every aspect ratio of that tier.
function estimateTokens(seconds, resolution) {
  const size = RESOLUTIONS[resolution];
  return (seconds * size.width * size.height * 24) / 1024;
}

function usageSchema(resolutions) {
  const resolutionLabels = {};
  for (const resolution of resolutions) resolutionLabels[resolution] = { en: resolution, zh: resolution };
  return {
    // Estimated at submission, settled on the tokens MaaS reports.
    tokens: {
      type: "number",
      unit: "token",
      description: { en: "Billing token unit price", zh: "计费 Token 单价" },
    },
    // Seedance prices tokens by output resolution tier.
    resolution: {
      enum: resolutions,
      enumLabels: resolutionLabels,
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
    // Seedance prices tokens of a request with a reference video lower.
    video_input: {
      enum: ["none", "video"],
      enumLabels: { none: { en: "No reference video", zh: "无参考视频" }, video: { en: "With reference video", zh: "有参考视频" } },
      description: { en: "Reference video input", zh: "参考视频输入" },
    },
  };
}

// Display examples: a 5-second video at each tier the model offers, and a
// 5-second video at the default resolution with a reference video.
function usageExamples(resolutions) {
  const examples = resolutions.map((resolution) => ({
    label: resolution + " · 5s",
    facts: { tokens: Math.round(estimateTokens(5, resolution)), resolution: resolution, video_input: "none" },
  }));
  examples.push({
    label: DEFAULT_RESOLUTION + " · 5s · reference video",
    facts: { tokens: Math.round(estimateTokens(5, DEFAULT_RESOLUTION)), resolution: DEFAULT_RESOLUTION, video_input: "video" },
  });
  return examples;
}

// One usage profile per set of resolution tiers: the pricing page lists only
// the tiers each model offers.
function usageProfiles() {
  const profiles = [];
  for (const name of Object.keys(ENDPOINTS)) {
    const resolutions = ENDPOINTS[name].resolutions;
    let profile = profiles.find((entry) => entry.schema.resolution.enum.join() === resolutions.join());
    if (!profile) {
      profile = { models: [], schema: usageSchema(resolutions), examples: usageExamples(resolutions) };
      profiles.push(profile);
    }
    profile.models.push(name);
  }
  return profiles;
}

export const meta = {
  apiVersion: 1,
  key: "mgtv",
  name: "MGTV MaaS",
  icon: "text:MaaS",
  description: {
    en: "Seedance video generation through the MGTV MaaS video API",
    zh: "通过芒果云 MaaS 视频接口生成 Seedance 视频",
  },
  version: "1.0.0",
  author: { name: "codzen" },
  baseUrl: "https://maas-api-pub.cloud.mgtv.com",
  upstreams: ["vendor"],
  models: Object.keys(ENDPOINTS),
  fetchMode: "per_task",
  usageSchema: usageSchema(Object.keys(RESOLUTIONS)),
  usageExamples: usageExamples(Object.keys(RESOLUTIONS)),
  usageProfiles: usageProfiles(),
  protocols: ["openai_video"],
};

// endpointFor finds the endpoint of the upstream model, which is the public
// model name, or the public model name an alias maps to. A MaaS model name is
// not a key: a channel mapping to one is refused. Error messages reach the
// caller, who must not learn the vendor or the MaaS model name.
function endpointFor(ctx) {
  const name = ctx.upstreamModel || ctx.model;
  if (!Object.hasOwn(ENDPOINTS, name)) throw new Error("this model is not available on this channel");
  return ENDPOINTS[name];
}

function quoted(value) {
  return typeof value === "string" ? JSON.stringify(value) : String(value);
}

// A whole number, written as a JSON number or a numeric string (OpenAI
// writes seconds as a string).
function wholeNumber(name, raw) {
  const value = typeof raw === "string" && /^\s*[+-]?\d+\s*$/.test(raw) ? Number(raw) : raw;
  if (typeof value !== "number" || !Number.isInteger(value)) throw new Error(name + " must be a whole number, got " + quoted(raw));
  return value;
}

// The aspect ratios a WIDTHxHEIGHT size can name, which are the W:H ratios
// the endpoints take (adaptive has no size), and the resolution tier of its
// short side.
const SIZE_ASPECT_RATIOS = [];
for (const endpoint of Object.values(ENDPOINTS)) {
  for (const ratio of endpoint.aspectRatios) {
    if (/^\d+:\d+$/.test(ratio) && SIZE_ASPECT_RATIOS.indexOf(ratio) < 0) SIZE_ASPECT_RATIOS.push(ratio);
  }
}
const SIZE_SHORT_SIDES = { 480: "480p", 720: "720p", 1080: "1080p", 2160: "4k" };

// The OpenAI size WIDTHxHEIGHT names a resolution by its short side and an
// aspect ratio by its width over height, within 1% (854x480 is 16:9).
function sizeParams(raw) {
  const match = typeof raw === "string" ? /^\s*(\d+)\s*[xX]\s*(\d+)\s*$/.exec(raw) : null;
  if (!match) throw new Error("size must be WIDTHxHEIGHT, such as 1280x720, got " + quoted(raw));
  const width = Number(match[1]);
  const height = Number(match[2]);
  const resolution = SIZE_SHORT_SIDES[Math.min(width, height)];
  const aspectRatio = SIZE_ASPECT_RATIOS.find((ratio) => {
    const [w, h] = ratio.split(":").map(Number);
    return Math.abs((width * h) / (height * w) - 1) <= 0.01;
  });
  if (!resolution || !aspectRatio) {
    throw new Error("size " + raw.trim() + " matches no resolution and aspect ratio; use a short side of 480, 720, 1080 or 2160 and an aspect ratio of " + SIZE_ASPECT_RATIOS.join(", "));
  }
  return { resolution: resolution, aspect_ratio: aspectRatio };
}

// A media URL MaaS fetches itself: an absolute http(s) URL.
function mediaURL(name, raw) {
  if (typeof raw !== "string" || !/^https?:\/\/[^\s/]+\S*$/i.test(raw.trim())) throw new Error(name + " must be an http(s) URL");
  return raw.trim();
}

// The media roles a caller may send. Audio roles have no price yet, and an
// asset_id or material_id would name another caller's asset on the shared
// MaaS account, so an item is exactly {type, url}.
const MEDIA_TYPES = ["first_frame", "last_frame", "reference_image", "reference_video"];

function mediaItems(raw) {
  if (!Array.isArray(raw)) throw new Error("media must be an array of {type, url} items");
  return raw.map((item, index) => {
    const name = "media[" + index + "]";
    if (!item || typeof item !== "object" || Array.isArray(item)) throw new Error(name + " must be a {type, url} object");
    const extra = Object.keys(item).filter((key) => key !== "type" && key !== "url");
    if (extra.length) throw new Error(name + " takes only type and url; remove " + extra.join(", "));
    if (MEDIA_TYPES.indexOf(item.type) < 0) throw new Error(name + ".type must be one of " + MEDIA_TYPES.join(", ") + ", got " + quoted(item.type));
    return { type: item.type, url: mediaURL(name + ".url", item.url) };
  });
}

// REQUEST_FIELDS maps every field the plugin takes, at the top level or
// under metadata, to the request parameters it sets on the endpoint, or to
// nothing when the endpoint does not take it. Two fields that set one
// parameter, or one field at the top level and under metadata, must agree.
// Any other field is refused, so no unpriced or unbounded field, nor an
// asset_id naming another caller's upstream asset, reaches MaaS.
const REQUEST_FIELDS = {
  prompt: (raw) => ({ prompt: raw }),
  seconds: (raw) => ({ duration: wholeNumber("seconds", raw) }),
  duration: (raw) => ({ duration: wholeNumber("duration", raw) }),
  resolution: (raw) => ({ resolution: String(raw).trim().toLowerCase() }),
  aspect_ratio: (raw) => ({ aspect_ratio: String(raw).trim() }),
  size: sizeParams,
  with_audio: (raw) => {
    if (typeof raw !== "boolean") throw new Error("with_audio must be true or false");
    return { with_audio: raw };
  },
  seed: (raw) => ({ seed: wholeNumber("seed", raw) }),
  output_format: (raw, endpoint) => (endpoint.outputFormats ? { output_format: raw } : null),
  // OpenAI's input_reference, and image_url, name the first frame.
  input_reference: (raw) => ({ first_frame: mediaURL("input_reference", raw) }),
  image_url: (raw) => ({ first_frame: mediaURL("image_url", raw) }),
  media: (raw) => ({ media: mediaItems(raw) }),
};

// Fields that configure the gateway, never the request.
const CONTROL_FIELDS = ["model", "metadata"];

// maasRequest validates a client request against the endpoint and returns
// the MaaS request body, the task action and the usage facts it bills. The
// decoder runs it to refuse a request at channel selection, and the submit
// and usage hooks run it again on the body they receive, which POST
// /v1/tasks/mgtv hands them without the decoder.
function maasRequest(endpoint, body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("the request body must be a JSON object");
  const metadata = body.metadata;
  if (metadata !== undefined && metadata !== null && (typeof metadata !== "object" || Array.isArray(metadata))) {
    throw new Error("metadata must be a JSON object");
  }
  const entries = [];
  for (const key of Object.keys(body)) {
    if (CONTROL_FIELDS.indexOf(key) < 0) entries.push({ label: key, key: key, raw: body[key] });
  }
  for (const key of Object.keys(metadata || {})) entries.push({ label: "metadata." + key, key: key, raw: metadata[key] });

  const params = {};
  const labels = {};
  const unsupported = [];
  for (const entry of entries) {
    // A null field is the endpoint's default.
    if (entry.raw === null || entry.raw === undefined) continue;
    const field = REQUEST_FIELDS[entry.key];
    const set = field ? field(entry.raw, endpoint) : null;
    if (!set) {
      if (unsupported.indexOf(entry.key) < 0) unsupported.push(entry.key);
      continue;
    }
    for (const param of Object.keys(set)) {
      if (Object.hasOwn(params, param)) {
        // media is an array: compare by value. mediaItems builds every item
        // with its keys in one order, so equal lists encode the same.
        if (JSON.stringify(params[param]) !== JSON.stringify(set[param])) throw new Error(labels[param] + " and " + entry.label + " give different values");
        continue;
      }
      params[param] = set[param];
      labels[param] = entry.label;
    }
  }
  if (unsupported.length) {
    throw new Error("parameter(s) " + unsupported.sort().join(", ") + " are not supported by this model; remove them");
  }

  if (typeof params.prompt !== "string" || params.prompt.trim() === "") throw new Error("prompt is required");

  // The duration multiplies the token estimate, so it is bounded here.
  const durations = endpoint.durations;
  const duration = Object.hasOwn(params, "duration") ? params.duration : durations.default;
  if (!(duration === -1 && durations.auto) && !(duration >= durations.min && duration <= durations.max)) {
    throw new Error(
      (labels.duration || "duration") + " must be " + (durations.auto ? "-1 or " : "") + "between " + durations.min + " and " + durations.max + " seconds"
    );
  }

  const resolution = params.resolution || DEFAULT_RESOLUTION;
  if (endpoint.resolutions.indexOf(resolution) < 0) {
    throw new Error("resolution must be one of " + endpoint.resolutions.join(", ") + " on this model, got " + quoted(resolution));
  }
  // extra_body keys are spelled as MaaS spells them: aspectRatio and
  // withAudio in camelCase, output_format and reference_mode in snake_case.
  // Sources: the VideoCreateReq.extra_body description in MaaS's OpenAPI
  // document ("可传 aspectRatio/fps/withAudio/webSearch/seed 等",
  // "extra_body.reference_mode 中传 multi_ref") and the modeProfiles
  // parameters GET /v1/models lists for the Seedance models (aspectRatio,
  // withAudio, output_format, resolution).
  const extraBody = { resolution: RESOLUTIONS[resolution].maas };
  if (Object.hasOwn(params, "aspect_ratio")) {
    if (endpoint.aspectRatios.indexOf(params.aspect_ratio) < 0) {
      throw new Error("aspect_ratio must be one of " + endpoint.aspectRatios.join(", ") + " on this model, got " + quoted(params.aspect_ratio));
    }
    extraBody.aspectRatio = params.aspect_ratio;
  }
  if (Object.hasOwn(params, "with_audio")) extraBody.withAudio = params.with_audio;
  if (Object.hasOwn(params, "seed")) extraBody.seed = params.seed;
  if (Object.hasOwn(params, "output_format")) {
    if (endpoint.outputFormats.indexOf(params.output_format) < 0) {
      throw new Error("output_format must be one of " + endpoint.outputFormats.join(", ") + ", got " + quoted(params.output_format));
    }
    extraBody.output_format = params.output_format;
  }

  // -1 lets the model pick the length; the reservation covers the longest
  // one and the task settles on the tokens MaaS reports.
  const billedSeconds = duration === -1 ? durations.max : duration;
  const maasBody = { model: endpoint.maasModel, prompt: params.prompt, seconds: duration, extra_body: extraBody };
  if (Object.hasOwn(params, "first_frame")) maasBody.input_image_url = params.first_frame;
  // Which media combine is MaaS's to judge: it refuses a bad combination
  // when it creates the task, before anything is charged.
  const media = params.media || [];
  const roleCounts = {};
  for (const item of media) roleCounts[item.type] = (roleCounts[item.type] || 0) + 1;
  for (const role of Object.keys(endpoint.maxMedia)) {
    const count = roleCounts[role] || 0;
    if (count > endpoint.maxMedia[role]) {
      throw new Error("media takes at most " + endpoint.maxMedia[role] + " " + role.replace("_", " ") + "s on this model, got " + count);
    }
  }
  if (media.length) maasBody.media = media;
  // Reference images alone are multi-image reference, a mode MaaS needs set.
  if (media.length && roleCounts.reference_image === media.length) extraBody.reference_mode = "multi_ref";
  const hasImageInput = Object.hasOwn(maasBody, "input_image_url") || media.length > 0;
  return {
    body: maasBody,
    action: hasImageInput ? "image_to_video" : "text_to_video",
    // A reference video selects its own token price; its length is not in
    // the estimate, and the task settles on the tokens MaaS reports.
    facts: { tokens: estimateTokens(billedSeconds, resolution), resolution: resolution, video_input: roleCounts.reference_video ? "video" : "none" },
  };
}

export function buildSubmitRequest(ctx) {
  const request = maasRequest(endpointFor(ctx), ctx.requestBody);
  return {
    url: ctx.baseUrl + "/v1/videos",
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: request.body,
    action: request.action,
  };
}

// Task expressions price the facts. Legacy per-call pricing gets no
// multipliers: the token count is not a ratio.
export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  return maasRequest(endpointFor(ctx), ctx.requestBody).facts;
}

// MaaS creates the task and answers queued; its id is the upstream task id.
// The GET /v1/videos responses MaaS returns are the bare video object, not
// the {code, data} envelope its OpenAPI document describes.
export function parseSubmitResponse(_ctx, resp) {
  const body = resp.body || {};
  if (typeof body.id !== "string" || body.id === "") throw new Error("upstream returned no task id");
  return { taskId: body.id, taskData: body };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

// The status words of the MaaS video object. Any other word is UNKNOWN,
// which the host counts as a failed poll.
const TASK_STATUSES = {
  queued: "SUBMITTED",
  pending: "SUBMITTED",
  in_progress: "IN_PROGRESS",
  processing: "IN_PROGRESS",
  running: "IN_PROGRESS",
  completed: "SUCCESS",
  succeeded: "SUCCESS",
  failed: "FAILURE",
  cancelled: "FAILURE",
  expired: "FAILURE",
};

function resultURL(body) {
  return body && typeof body.url === "string" ? body.url.trim() : "";
}

export function parseTaskResult(_ctx, body) {
  const data = body || {};
  const status = TASK_STATUSES[data.status];
  if (!status) return { status: "UNKNOWN", reason: "unknown task status: " + String(data.status || "") };
  if (status === "SUCCESS") {
    // A success with nothing to deliver fails, so the host refunds it.
    const url = resultURL(data);
    if (!url) return { status: "FAILURE", reason: "the task completed without producing a video" };
    return { status: status, progress: "100%", url: url };
  }
  if (status === "FAILURE") {
    // MaaS's own message, as it is: callers need it to decide whether to
    // retry or change the request (ADR-0003).
    const message = data.error && typeof data.error.message === "string" ? data.error.message : "";
    return { status: status, reason: message || "task " + data.status };
  }
  const result = { status: status };
  if (data.progress > 0 && data.progress < 100) result.progress = data.progress + "%";
  return result;
}

// Settlement replaces the reserved tokens with the ones MaaS reports, and
// keeps resolution and video_input from submission. Missing or invalid
// usage returns no facts, which keeps the reservation.
export function extractUsageOnComplete(_ctx, result, body) {
  if (!result || result.status !== "SUCCESS") return null;
  const usage = (body && body.usage) || {};
  for (const tokens of [usage.completion_tokens, usage.total_tokens]) {
    if (typeof tokens === "number" && Number.isFinite(tokens) && tokens > 0) return { tokens: tokens };
  }
  return null;
}

// A completed task's video is its one artifact, which GET
// /v1/videos/{task_id}/content streams. The stored url is a signed COS link
// valid for 24 hours that needs no credentials; MaaS's own /content only
// redirects to the same link, so the gateway fetches the link directly and
// never sends the channel key to it.
export function listArtifacts(task) {
  return task.status === "SUCCESS" && resultURL(task.data) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  const url = ctx.artifactKey === "video" ? resultURL(ctx.data) : "";
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export const protocols = {
  openai_video: {
    // POST /v1/videos takes a JSON object. The decoder refuses an invalid
    // request at channel selection and hands the driver hooks the client's
    // body, which they validate again.
    decodeRequest(ctx) {
      const body = ctx.body;
      if (!body || body.kind !== "json") throw new Error("a JSON request body is required");
      const request = maasRequest(endpointFor(ctx), body.value);
      return { kind: "submit", model: ctx.model, action: request.action, requestBody: Object.assign({}, body.value, { model: ctx.model }) };
    },
    // The host fills in id, object, model, status, progress and the
    // timestamps; the plugin adds only the failure. Nothing of the MaaS
    // video object goes out: its url, usage, provider_task_id,
    // provider_usage and provider_result_url name the vendor or let the
    // caller download around the gateway.
    render(_ctx, task) {
      const video = {};
      if (task.status === "FAILURE") video.error = { message: task.fail_reason || "", code: "task_failed" };
      return video;
    },
  },
};
