// MuleRouter endpoints, keyed by public model name. Callers never see the
// MuleRouter model name ({vendor}/{model}/{action}) each one names under
// mulerouterModel, from which its upstream path is built, so MuleRouter
// channels carry no model_mapping (ADR-0002). Prices are keyed by the public
// model name, and a request is charged that price times the multipliers its
// fields select here.
//
// Each endpoint declares what it produces (image or video), the action its
// tasks are filed under on every entry, and, under fields, every request
// field, by its MuleRouter name, that affects the price or needs a bound:
// - values maps each allowed value to its multiplier, which the host receives
//   under the name ratio; default is the value an omitted or null field is
//   billed as.
// - min and max bound a field that does not affect the price.
// - integer requires a whole number, written as a JSON number or a numeric
//   string.
// An endpoint that produces videos also lists under params the other request
// fields MuleRouter takes; the OpenAI video API refuses any field outside
// fields and params.
// Request validation and the billing multipliers are both derived from this
// table, and the OpenAI video API serves the endpoints that produce videos.
const ENDPOINTS = {
  "z-image-spicy": {
    mulerouterModel: "carrothub/z-image-spicy/generation",
    produces: "image",
    action: "text_to_image",
    fields: {
      // MuleRouter adds a flat fee for prompt rewriting: $0.001 on a $0.013 image.
      prompt_extend: { ratio: "prompt_extend", values: { true: 1.076923, false: 1 }, default: true },
      width: { integer: true, min: 256, max: 1536 },
      height: { integer: true, min: 256, max: 1536 },
    },
  },
  // Image edit: one flat price per call.
  "qwen-image-edit-spicy": {
    mulerouterModel: "carrothub/qwen-image-edit-spicy/generation",
    produces: "image",
    action: "image_to_image",
    fields: {},
  },
  // Image to video, priced per second of output and by resolution.
  "wan2.2-i2v-spicy": {
    mulerouterModel: "carrothub/wan2.2-i2v-spicy/generation",
    produces: "video",
    action: "image_to_video",
    fields: {
      duration: { ratio: "seconds", integer: true, values: { 5: 5, 8: 8 }, default: 5 },
      resolution: { ratio: "resolution", values: { "480p": 1, "720p": 2 }, default: "480p" },
    },
    params: ["prompt", "image", "last_image", "prompt_extend", "seed"],
  },
};

// Request fields known to scale MuleRouter's cost. An endpoint that does not
// declare one of them refuses it, so an unpriced dimension never reaches the
// vendor as an unbounded multiplier.
const COST_FIELDS = [
  "n",
  "count",
  "num_images",
  "num_outputs",
  "batch_size",
  "duration",
  "seconds",
  "resolution",
  "size",
  "width",
  "height",
  "quality",
  "fps",
  "frames",
  "num_frames",
  "steps",
  "sample_count",
];

// Fields that configure the gateway, never the vendor endpoint.
const CONTROL_FIELDS = ["model", "metadata"];

export const meta = {
  apiVersion: 1,
  key: "mulerouter",
  name: "MuleRouter",
  description: {
    en: "Asynchronous image and video generation through MuleRouter",
    zh: "通过 MuleRouter 异步生成图片和视频",
  },
  version: "1.3.0",
  author: { name: "codzen" },
  baseUrl: "https://api.mulerouter.ai",
  upstreams: ["vendor"],
  models: Object.keys(ENDPOINTS),
  fetchMode: "per_task",
  // The OpenAI video API serves only the endpoints that produce videos.
  protocols: [{ name: "openai_video", models: Object.keys(ENDPOINTS).filter((name) => ENDPOINTS[name].produces === "video") }],
};

// endpointFor finds the endpoint of the upstream model, which is the public
// model name, or the public model name an alias maps to. A MuleRouter model
// name is not a key: a channel mapping to one, or a task submitted under one,
// is refused. Error messages reach the caller, who must not learn the vendor
// or the MuleRouter model name behind the public model name.
function endpointFor(ctx) {
  const name = ctx.upstreamModel || ctx.model;
  if (!Object.hasOwn(ENDPOINTS, name)) throw new Error("this model is not available on this channel");
  const endpoint = ENDPOINTS[name];
  const [vendor, model, action] = endpoint.mulerouterModel.split("/");
  return {
    path: "/vendors/" + vendor + "/v1/" + model + "/" + action,
    action: endpoint.action,
    fields: endpoint.fields,
    params: endpoint.params || [],
  };
}

// The upstream body is the request minus the gateway's own fields, with vendor
// parameters from metadata merged over it.
function upstreamParams(requestBody) {
  const params = {};
  const body = requestBody || {};
  for (const key of Object.keys(body)) params[key] = body[key];
  const metadata = body.metadata;
  if (metadata && typeof metadata === "object" && !Array.isArray(metadata)) {
    for (const key of Object.keys(metadata)) params[key] = metadata[key];
  }
  for (const key of CONTROL_FIELDS) delete params[key];
  return params;
}

function quoted(value) {
  return JSON.stringify(String(value));
}

// billingRatios validates the body that will be sent upstream against the
// endpoint's fields and returns the multipliers they select, so metadata
// cannot carry a multiplier past it.
function billingRatios(endpoint, params) {
  const ratios = {};
  for (const name of Object.keys(endpoint.fields)) {
    const field = endpoint.fields[name];
    let raw = params[name];
    if (raw === undefined || raw === null) {
      if (field.default === undefined) continue;
      raw = field.default;
    }
    let key;
    if (field.integer) {
      let value = raw;
      if (typeof raw === "string") {
        const text = raw.trim();
        if (!/^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(text)) {
          throw new Error("invalid value for " + name + ": " + quoted(raw) + " is not a number");
        }
        value = Number(text);
      } else if (typeof raw !== "number") {
        throw new Error("invalid value for " + name + ": " + String(raw) + " is not a number");
      }
      if (!Number.isInteger(value)) throw new Error(name + " must be an integer, got " + String(raw));
      if (field.min !== undefined && value < field.min) throw new Error(name + " must be >= " + String(field.min));
      if (field.max !== undefined && value > field.max) throw new Error(name + " must be <= " + String(field.max));
      // A field without values is bounded but does not affect the price.
      if (!field.values) continue;
      key = String(value);
      // Integer keys enumerate in ascending order.
      if (!Object.hasOwn(field.values, key)) {
        throw new Error("invalid value " + key + " for " + name + ", allowed: " + Object.keys(field.values).join(", "));
      }
    } else {
      key = String(raw);
      if (!Object.hasOwn(field.values, key)) {
        throw new Error("invalid value " + quoted(key) + " for " + name + ", allowed: " + Object.keys(field.values).sort().join(", "));
      }
    }
    const ratio = field.values[key];
    if (!(ratio > 0) || !Number.isFinite(ratio)) throw new Error(name + " produced a non-positive billing multiplier");
    ratios[field.ratio] = ratio;
  }
  const undeclared = Object.keys(params)
    .filter(
      (key) =>
        !Object.hasOwn(endpoint.fields, key) &&
        params[key] !== null &&
        params[key] !== undefined &&
        COST_FIELDS.indexOf(key) >= 0
    )
    .sort();
  if (undeclared.length) {
    throw new Error(
      "parameter(s) " + undeclared.join(", ") + " affect the price of this model and are not enabled on this endpoint; remove them"
    );
  }
  return ratios;
}

// Every MuleRouter endpoint takes a prompt, so a task without one is refused
// before charging. buildSubmitRequest applies it on every entry,
// before quota is reserved; the openai_video decoder applies it again so POST
// /v1/videos refuses the request already at channel selection.
function requirePrompt(body) {
  if (typeof body.prompt !== "string" || body.prompt.trim() === "") throw new Error("prompt is required");
}

export function buildSubmitRequest(ctx) {
  const endpoint = endpointFor(ctx);
  const params = upstreamParams(ctx.requestBody);
  requirePrompt(params);
  billingRatios(endpoint, params);
  return {
    url: ctx.baseUrl + endpoint.path,
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: params,
    action: endpoint.action,
  };
}

// Per-call pricing: the public model's fixed price times these multipliers.
export function extractUsage(ctx) {
  if (ctx.usagePurpose !== "billing_ratios") return null;
  return billingRatios(endpointFor(ctx), upstreamParams(ctx.requestBody));
}

// failureReason reads a task's error: the carrothub endpoints put it under
// task_info.error with a code, the async task guide at the top-level error
// with an error_code. It returns the title, the detail and the code of the
// first of the two that has any, or "" when neither does.
function failureReason(body) {
  const info = (body && body.task_info) || {};
  for (const error of [info.error, body && body.error]) {
    if (!error || typeof error !== "object") continue;
    let reason = error.title && error.detail ? error.title + ": " + error.detail : error.title || error.detail || "";
    const code = error.error_code !== undefined && error.error_code !== null ? error.error_code : error.code;
    if (code !== undefined && code !== null && code !== "") reason += (reason ? " " : "") + "(error code " + String(code) + ")";
    if (reason) return reason;
  }
  return "";
}

// A submission fails when MuleRouter says so, or when it returns an error
// instead of a task.
export function parseSubmitResponse(_ctx, resp) {
  const info = resp.body && resp.body.task_info;
  const reason = failureReason(resp.body);
  if (info && info.status === "failed") throw new Error("task failed: " + (reason || "no reason given"));
  if (!info || !info.id) throw new Error(reason ? "task failed: " + reason : "upstream returned no task id");
  return { taskId: info.id, taskData: resp.body };
}

// MuleRouter files a task under its endpoint's path, so the endpoint of the
// stored upstream model rebuilds the query URL.
export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + endpointFor(ctx).path + "/" + ctx.taskId,
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

// The carrothub endpoint YAML says pending / processing / completed / failed,
// and the carrothub wan2.2-i2v-spicy YAML adds retrying; the async task guide
// says queued / running / succeeded / failed. Any other word is UNKNOWN,
// which the host counts as a failed poll.
const TASK_STATUSES = {
  pending: "SUBMITTED",
  queued: "SUBMITTED",
  processing: "IN_PROGRESS",
  running: "IN_PROGRESS",
  retrying: "IN_PROGRESS",
  completed: "SUCCESS",
  succeeded: "SUCCESS",
  failed: "FAILURE",
};

// firstURL is the first non-empty string of a result list, or "".
function firstURL(list) {
  return (Array.isArray(list) && list.find((item) => typeof item === "string" && item !== "")) || "";
}

// A completed task carries its results under images, videos or audios.
function resultURL(body) {
  for (const key of ["images", "videos", "audios"]) {
    const url = firstURL(body[key]);
    if (url) return url;
  }
  return "";
}

export function parseTaskResult(_ctx, body) {
  const info = (body && body.task_info) || {};
  const status = TASK_STATUSES[info.status];
  if (!status) return { status: "UNKNOWN", reason: "unknown task status: " + String(info.status || "") };
  if (status === "SUCCESS") {
    // A success with nothing to deliver fails, so the host refunds it.
    const url = resultURL(body);
    if (!url) return { status: "FAILURE", reason: "the task completed without producing any image, video or audio" };
    return { status: status, url: url };
  }
  if (status === "FAILURE") return { status: status, reason: failureReason(body) || "task failed" };
  return { status: status };
}

function resultVideoURL(data) {
  return firstURL(data && data.videos);
}

function resultImageURLs(data) {
  if (!data || !Array.isArray(data.images)) return [];
  return data.images.filter((url) => typeof url === "string" && url !== "");
}

// An image artifact key is this prefix and the image's 1-based position, with
// no leading zeros: image-1, image-2, ... listArtifacts writes it and
// buildContentRequest reads it back.
const IMAGE_ARTIFACT_PREFIX = "image-";

// A completed task's results are its artifacts: every image as image-1,
// image-2, ... in MuleRouter's order, or the first video as video, which
// GET /v1/videos/{task_id}/content also streams. The URLs are MuleRouter's
// public result links and need no credentials. The host refuses to load a
// plugin that claims openai_video without these two hooks.
export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const images = resultImageURLs(task.data);
  if (images.length) return images.map((_, index) => ({ key: IMAGE_ARTIFACT_PREFIX + (index + 1), type: "image" }));
  return resultVideoURL(task.data) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  const key = String(ctx.artifactKey || "");
  let url = "";
  if (key === "video") url = resultVideoURL(ctx.data);
  else if (key.startsWith(IMAGE_ARTIFACT_PREFIX)) {
    const position = key.slice(IMAGE_ARTIFACT_PREFIX.length);
    if (/^[1-9][0-9]*$/.test(position)) url = resultImageURLs(ctx.data)[Number(position) - 1] || "";
  }
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

// OPENAI_VIDEO_FIELDS maps the OpenAI video fields to the MuleRouter
// parameter they spell, and normalizes their value.
const OPENAI_VIDEO_FIELDS = {
  seconds: { param: "duration", value: integerAsNumber },
  // The short side of WIDTHxHEIGHT names the resolution: 1280x720 is 720p.
  size: {
    param: "resolution",
    value: (raw) => {
      const match = typeof raw === "string" ? /^\s*(\d+)\s*[xX]\s*(\d+)\s*$/.exec(raw) : null;
      if (!match) throw new Error("size must be WIDTHxHEIGHT, such as 1280x720, got " + (typeof raw === "string" ? quoted(raw) : String(raw)));
      return String(Math.min(Number(match[1]), Number(match[2]))) + "p";
    },
  },
  input_reference: { param: "image", value: (raw) => requireImageURL("input_reference", raw) },
};

// The vendor spellings normalized the same way, so two spellings of one value
// agree.
const VENDOR_VALUES = {
  duration: integerAsNumber,
  image: (raw) => requireImageURL("image", raw),
};

// A whole number written as a string is sent as a number, which MuleRouter
// requires; anything else is left for the endpoint's field check to refuse.
function integerAsNumber(raw) {
  return typeof raw === "string" && /^\s*[+-]?\d+\s*$/.test(raw) ? Number(raw) : raw;
}

// The JSON API takes the input image as a URL only.
function requireImageURL(name, raw) {
  if (typeof raw !== "string" || !/^https?:\/\/\S+$/i.test(raw.trim())) throw new Error(name + " must be an image URL");
  return raw.trim();
}

export const protocols = {
  openai_video: {
    // POST /v1/videos takes a JSON object with the OpenAI video fields and
    // MuleRouter's vendor parameters, at the top level or under metadata, and
    // hands the endpoint the flat body it sends.
    decodeRequest(ctx) {
      const body = ctx.body;
      if (!body || body.kind !== "json") throw new Error("a JSON request body is required");
      const value = body.value;
      if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("the request body must be a JSON object");
      const metadata = value.metadata;
      if (metadata !== undefined && metadata !== null && (typeof metadata !== "object" || Array.isArray(metadata))) {
        throw new Error("metadata must be a JSON object");
      }
      const endpoint = endpointFor(ctx);
      const entries = [];
      for (const key of Object.keys(value)) {
        if (CONTROL_FIELDS.indexOf(key) < 0) entries.push({ label: key, key: key, raw: value[key] });
      }
      for (const key of Object.keys(metadata || {})) entries.push({ label: "metadata." + key, key: key, raw: metadata[key] });

      const params = {};
      const labels = {};
      const unsupported = [];
      for (const entry of entries) {
        // A null field is the endpoint's default.
        if (entry.raw === null || entry.raw === undefined) continue;
        const alias = OPENAI_VIDEO_FIELDS[entry.key];
        const param = alias ? alias.param : entry.key;
        if (!Object.hasOwn(endpoint.fields, param) && endpoint.params.indexOf(param) < 0) {
          if (unsupported.indexOf(entry.key) < 0) unsupported.push(entry.key);
          continue;
        }
        const normalize = alias ? alias.value : VENDOR_VALUES[param];
        const normalized = normalize ? normalize(entry.raw) : entry.raw;
        if (Object.hasOwn(params, param)) {
          if (JSON.stringify(params[param]) !== JSON.stringify(normalized)) {
            throw new Error(labels[param] + " and " + entry.label + " give different values");
          }
          continue;
        }
        params[param] = normalized;
        labels[param] = entry.label;
      }
      if (unsupported.length) {
        throw new Error("parameter(s) " + unsupported.sort().join(", ") + " are not supported by this model; remove them");
      }
      requirePrompt(params);
      params.model = ctx.model;
      return { kind: "submit", model: ctx.model, action: endpoint.action, requestBody: params };
    },
    // The host fills in id, object, model, status, progress and the
    // timestamps; the plugin adds the result URL and the failure.
    render(_ctx, task) {
      const data = task.data || {};
      for (const key of ["images", "audios"]) {
        if (Array.isArray(data[key]) && data[key].length) throw new Error("this model does not produce videos");
      }
      const video = {};
      const url = resultVideoURL(data);
      if (url) video.metadata = { url: url };
      if (task.status === "FAILURE") video.error = { message: task.fail_reason || "", code: "task_failed" };
      return video;
    },
  },
};
