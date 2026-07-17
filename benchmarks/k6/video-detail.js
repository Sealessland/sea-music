import http from "k6/http";
import { check } from "k6";
import exec from "k6/execution";
import { SharedArray } from "k6/data";

const baseURL = requiredEnv("BASE_URL").replace(/\/$/, "");
const targetsFile = __ENV.TARGETS_FILE || "./targets.json";
const pattern = __ENV.ACCESS_PATTERN || "pareto80";
const rate = positiveInt("RATE", 2000);
const duration = __ENV.DURATION || "60s";
const preAllocatedVUs = positiveInt("PRE_ALLOCATED_VUS", 128);
const maxVUs = positiveInt("MAX_VUS", 1024);

const videoIDs = new SharedArray("video IDs", () => {
  const values = JSON.parse(open(targetsFile));
  if (!Array.isArray(values) || values.length === 0) {
    throw new Error("TARGETS_FILE must contain a non-empty JSON array");
  }
  return values;
});

export const options = {
  discardResponseBodies: true,
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
  scenarios: {
    video_detail: {
      executor: "constant-arrival-rate",
      rate,
      timeUnit: "1s",
      duration,
      preAllocatedVUs,
      maxVUs,
      gracefulStop: "15s",
    },
  },
  thresholds: {
    checks: ["rate>0.999"],
    http_req_failed: ["rate<0.001"],
    http_req_duration: [
      `p(95)<${positiveInt("P95_LIMIT_MS", 50)}`,
      `p(99)<${positiveInt("P99_LIMIT_MS", 100)}`,
    ],
    dropped_iterations: ["count==0"],
  },
};

export default function () {
  const sequence = exec.scenario.iterationInTest;
  const videoID = selectVideoID(sequence, videoIDs, pattern);
  const response = http.get(`${baseURL}/api/v1/videos/${videoID}`, {
    tags: { endpoint: "video-detail", access_pattern: pattern },
  });
  check(response, { "video detail is 200": (result) => result.status === 200 });
}

export function handleSummary(data) {
  const summary = {
    schema_version: 1,
    generated_at: new Date().toISOString(),
    config: {
      variant: __ENV.VARIANT || "unspecified",
      repeat: positiveInt("REPEAT", 1),
      target_rps: rate,
      duration,
      access_pattern: pattern,
      target_count: videoIDs.length,
      pre_allocated_vus: preAllocatedVUs,
      max_vus: maxVUs,
    },
    metrics: {
      http_reqs_count: metric(data, "http_reqs", "count"),
      http_reqs_rate: metric(data, "http_reqs", "rate"),
      iterations_count: metric(data, "iterations", "count"),
      dropped_iterations: metric(data, "dropped_iterations", "count"),
      checks_rate: metric(data, "checks", "rate"),
      failed_rate: metric(data, "http_req_failed", "rate"),
      duration_ms: {
        avg: metric(data, "http_req_duration", "avg"),
        min: metric(data, "http_req_duration", "min"),
        median: metric(data, "http_req_duration", "med"),
        p90: metric(data, "http_req_duration", "p(90)"),
        p95: metric(data, "http_req_duration", "p(95)"),
        p99: metric(data, "http_req_duration", "p(99)"),
        max: metric(data, "http_req_duration", "max"),
      },
      vus_max: metric(data, "vus_max", "max"),
      data_received_bytes: metric(data, "data_received", "count"),
    },
    thresholds: thresholdResults(data),
  };
  return {
    [requiredEnv("SUMMARY_PATH")]: `${JSON.stringify(summary, null, 2)}\n`,
    stdout: `${JSON.stringify(summary)}\n`,
  };
}

function selectVideoID(sequence, values, selectedPattern) {
  if (selectedPattern === "hot") {
    return values[0];
  }
  if (selectedPattern === "uniform") {
    return values[sequence % values.length];
  }
  if (selectedPattern !== "pareto80") {
    throw new Error(`unsupported ACCESS_PATTERN ${selectedPattern}`);
  }
  const hotCount = Math.max(1, Math.floor(values.length * 0.2));
  const slot = sequence % 10;
  if (slot < 8 || hotCount === values.length) {
    return values[(Math.floor(sequence / 10) * 8 + slot) % hotCount];
  }
  const coldCount = values.length - hotCount;
  return values[hotCount + ((Math.floor(sequence / 10) * 2 + slot - 8) % coldCount)];
}

function metric(data, name, value) {
  return data.metrics[name] && data.metrics[name].values[value] !== undefined
    ? data.metrics[name].values[value]
    : 0;
}

function thresholdResults(data) {
  const results = {};
  for (const [metricName, metricValue] of Object.entries(data.metrics)) {
    if (!metricValue.thresholds) {
      continue;
    }
    results[metricName] = {};
    for (const [name, value] of Object.entries(metricValue.thresholds)) {
      results[metricName][name] = !value.ok;
    }
  }
  return results;
}

function positiveInt(name, fallback) {
  const raw = __ENV[name];
  if (raw === undefined || raw === "") {
    return fallback;
  }
  const value = Number.parseInt(raw, 10);
  if (!Number.isInteger(value) || value <= 0) {
    throw new Error(`${name} must be a positive integer`);
  }
  return value;
}

function requiredEnv(name) {
  const value = __ENV[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}
