import http from "k6/http";
import { check, sleep } from "k6";

const sharedVus = integerEnvironment("VUS", 1);
const sharedDuration = __ENV.DURATION || "15m";
const minimumDelay = numberEnvironment("MIN_DELAY", 15);
const maximumDelay = numberEnvironment("MAX_DELAY", 30);

export const options = {
  scenarios: {
    travel: {
      executor: "constant-vus",
      exec: "travel",
      vus: integerEnvironment("TRAVEL_VUS", sharedVus),
      duration: __ENV.TRAVEL_DURATION || sharedDuration,
    },
    recipe: {
      executor: "constant-vus",
      exec: "recipe",
      vus: integerEnvironment("RECIPE_VUS", sharedVus),
      duration: __ENV.RECIPE_DURATION || sharedDuration,
    },
    entertainment: {
      executor: "constant-vus",
      exec: "entertainment",
      vus: integerEnvironment("ENTERTAINMENT_VUS", sharedVus),
      duration: __ENV.ENTERTAINMENT_DURATION || sharedDuration,
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
  },
};

const urls = {
  travel: __ENV.TRAVEL_URL || "http://localhost:8081",
  recipe: __ENV.RECIPE_URL || "http://localhost:8082",
  entertainment: __ENV.ENTERTAINMENT_URL || "http://localhost:8083",
};

export function travel() {
  simulate("travel", urls.travel);
}

export function recipe() {
  simulate("recipe", urls.recipe);
}

export function entertainment() {
  simulate("entertainment", urls.entertainment);
}

function simulate(role, baseUrl) {
  const response = http.post(`${baseUrl}/v1/simulate`, "{}", {
    headers: { "Content-Type": "application/json" },
    tags: { role },
    timeout: "15m",
  });
  check(response, {
    [`${role} simulation completed`]: (result) => result.status === 200,
  });
  sleep(minimumDelay + Math.random() * (maximumDelay - minimumDelay));
}

function integerEnvironment(name, fallback) {
  const raw = __ENV[name];
  if (raw === undefined) return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 1) {
    throw new Error(`${name} must be a positive integer`);
  }
  return value;
}

function numberEnvironment(name, fallback) {
  const raw = __ENV[name];
  if (raw === undefined) return fallback;
  const value = Number(raw);
  if (!Number.isFinite(value) || value < 0) {
    throw new Error(`${name} must be a non-negative number`);
  }
  return value;
}

if (maximumDelay < minimumDelay) {
  throw new Error("MAX_DELAY must be greater than or equal to MIN_DELAY");
}

