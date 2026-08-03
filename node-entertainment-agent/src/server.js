#!/usr/bin/env node
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";

import express from "express";

import {
  EntertainmentAgent,
  OllamaError,
  OllamaTimeoutError,
} from "./agent.js";
import { loadConfig } from "./config.js";
import { summarizeToolResult } from "./tools.js";

const maxBodyBytes = 32 * 1024;
const maxMessageChars = 8_000;
const scenarios = JSON.parse(
  readFileSync(new URL("../data/scenarios.json", import.meta.url), "utf8"),
);
const agentConfig = loadConfig([]).config;
const serviceConfig = {
  host: process.env.HTTP_HOST ?? "0.0.0.0",
  port: envInteger("HTTP_PORT", 8083),
  sessionTtl: envNumber("SESSION_TTL", 1800) * 1000,
  maxSessions: envInteger("MAX_SESSIONS", 100),
  maxConcurrentRequests: envInteger("MAX_CONCURRENT_REQUESTS", 4),
  chatTimeout: envNumber("CHAT_TIMEOUT", 300) * 1000,
  simulateTimeout: envNumber("SIMULATE_TIMEOUT", 900) * 1000,
};
validateServiceConfig(serviceConfig);

class APIError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

class SessionStore {
  constructor() {
    this.sessions = new Map();
  }

  acquire(requestedId) {
    const now = Date.now();
    this.#expire(now);
    if (requestedId !== undefined) {
      const session = this.sessions.get(requestedId);
      if (!session) throw new APIError(404, "session_not_found", "unknown session_id");
      session.active += 1;
      session.lastUsed = now;
      return [requestedId, session];
    }

    if (this.sessions.size >= serviceConfig.maxSessions) {
      const idle = [...this.sessions.entries()].filter(([, session]) => session.active === 0);
      if (!idle.length) {
        throw new APIError(429, "session_capacity", "session capacity reached");
      }
      idle.sort((left, right) => left[1].lastUsed - right[1].lastUsed);
      this.sessions.delete(idle[0][0]);
    }
    const id = randomUUID();
    const session = {
      agent: new EntertainmentAgent(agentConfig),
      active: 1,
      lastUsed: now,
      tail: Promise.resolve(),
    };
    this.sessions.set(id, session);
    return [id, session];
  }

  release(session) {
    session.active = Math.max(0, session.active - 1);
    session.lastUsed = Date.now();
  }

  delete(id) {
    this.sessions.delete(id);
  }

  #expire(now) {
    for (const [id, session] of this.sessions) {
      if (session.active === 0 && now - session.lastUsed > serviceConfig.sessionTtl) {
        this.sessions.delete(id);
      }
    }
  }
}

const app = express();
const sessionStore = new SessionStore();
let inFlight = 0;

app.disable("x-powered-by");
app.use(express.json({ limit: maxBodyBytes, strict: true }));

app.get("/healthz", (_request, response) => {
  response.json({ status: "ok", service: "entertainment" });
});

app.get("/readyz", async (_request, response) => {
  const timeout = Math.min(agentConfig.timeout * 1000, 5_000);
  let ollamaResponse;
  try {
    ollamaResponse = await fetch(`${agentConfig.ollamaHost}/api/tags`, {
      signal: AbortSignal.timeout(timeout),
    });
  } catch (error) {
    throw new APIError(503, "not_ready", "Ollama is unavailable");
  }
  if (!ollamaResponse.ok) {
    throw new APIError(503, "not_ready", "Ollama is unavailable");
  }
  let payload;
  try {
    payload = await ollamaResponse.json();
  } catch {
    throw new APIError(503, "not_ready", "Ollama returned an invalid response");
  }
  const available = (payload.models ?? []).some(
    (model) => model.name === agentConfig.model || model.model === agentConfig.model,
  );
  if (!available) {
    throw new APIError(503, "not_ready", `model ${agentConfig.model} is unavailable`);
  }
  response.json({ status: "ready", service: "entertainment", model: agentConfig.model });
});

app.post("/v1/chat", async (request, response) => {
  const body = objectBody(request.body);
  rejectUnknown(body, ["message", "session_id"]);
  if (typeof body.message !== "string" || !body.message.trim()) {
    throw new APIError(400, "invalid_request", "message must be a non-empty string");
  }
  if ([...body.message].length > maxMessageChars) {
    throw new APIError(400, "invalid_request", "message exceeds 8000 characters");
  }
  if (
    body.session_id !== undefined &&
    (typeof body.session_id !== "string" || !body.session_id.trim())
  ) {
    throw new APIError(400, "invalid_request", "session_id must be a non-empty string");
  }
  enter();
  const deadline = Date.now() + serviceConfig.chatTimeout;
  let session;
  try {
    const [sessionId, acquired] = sessionStore.acquire(body.session_id?.trim());
    session = acquired;
    const result = await withSessionLock(session, deadline, async () => {
      const tools = [];
      const answer = await session.agent.ask(
        body.message.trim(),
        (name, args, toolResult) => {
          tools.push({
            name,
            arguments: args,
            summary: summarizeToolResult(toolResult),
          });
        },
        deadline,
      );
      return { session_id: sessionId, answer, tools };
    });
    response.json(result);
  } catch (error) {
    throw mapAgentError(error);
  } finally {
    if (session) sessionStore.release(session);
    leave();
  }
});

app.post("/v1/simulate", async (request, response) => {
  const body = request.body === undefined ? {} : objectBody(request.body);
  rejectUnknown(body, ["scenario", "seed"]);
  const selected = selectScenario(body);
  enter();
  const deadline = Date.now() + serviceConfig.simulateTimeout;
  try {
    const agent = new EntertainmentAgent(agentConfig);
    const transcript = [];
    for (const message of selected.prompts) {
      const tools = [];
      const answer = await agent.ask(
        message,
        (name, args, toolResult) => {
          tools.push({
            name,
            arguments: args,
            summary: summarizeToolResult(toolResult),
          });
        },
        deadline,
      );
      transcript.push({ message, answer, tools });
    }
    response.json({ scenario: selected.name, transcript });
  } catch (error) {
    throw mapAgentError(error);
  } finally {
    leave();
  }
});

app.delete("/v1/sessions/:sessionId", (request, response) => {
  sessionStore.delete(request.params.sessionId);
  response.status(204).end();
});

for (const path of ["/healthz", "/readyz", "/v1/chat", "/v1/simulate"]) {
  app.all(path, (_request, _response) => {
    throw new APIError(405, "method_not_allowed", "method not allowed");
  });
}
app.all("/v1/sessions/:sessionId", (_request, _response) => {
  throw new APIError(405, "method_not_allowed", "method not allowed");
});

app.use((_request, _response) => {
  throw new APIError(404, "not_found", "not found");
});

app.use((error, _request, response, _next) => {
  if (error?.type === "entity.too.large") {
    return sendError(response, 400, "invalid_request", "request body exceeds 32 KiB");
  }
  if (error instanceof SyntaxError && "body" in error) {
    return sendError(response, 400, "invalid_json", "request body must be valid JSON");
  }
  if (error instanceof APIError) {
    return sendError(response, error.status, error.code, error.message);
  }
  return sendError(response, 500, "internal_error", "unexpected service error");
});

const server = app.listen(serviceConfig.port, serviceConfig.host, () => {
  console.log(
    `entertainment service listening on ${serviceConfig.host}:${serviceConfig.port}`,
  );
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => {
    const force = setTimeout(() => {
      server.closeAllConnections();
      process.exit(1);
    }, 10_000);
    force.unref();
    server.close(() => {
      clearTimeout(force);
      process.exit(0);
    });
  });
}

function enter() {
  if (inFlight >= serviceConfig.maxConcurrentRequests) {
    throw new APIError(429, "too_many_requests", "service concurrency limit reached");
  }
  inFlight += 1;
}

function leave() {
  inFlight = Math.max(0, inFlight - 1);
}

async function withSessionLock(session, deadline, operation) {
  const previous = session.tail;
  let release;
  session.tail = new Promise((resolve) => {
    release = resolve;
  });
  const remaining = deadline - Date.now();
  if (remaining <= 0) {
    previous.then(release);
    throw new OllamaTimeoutError("request deadline exceeded");
  }
  let timer;
  try {
    await Promise.race([
      previous,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(new OllamaTimeoutError("request deadline exceeded")),
          remaining,
        );
      }),
    ]);
  } catch (error) {
    previous.then(release);
    throw error;
  } finally {
    clearTimeout(timer);
  }
  try {
    return await operation();
  } finally {
    release();
  }
}

function selectScenario(body) {
  if (body.scenario !== undefined) {
    if (typeof body.scenario !== "string" || !body.scenario.trim()) {
      throw new APIError(400, "invalid_request", "scenario must be a non-empty string");
    }
    const selected = scenarios.find((scenario) => scenario.name === body.scenario.trim());
    if (!selected) {
      throw new APIError(400, "unknown_scenario", `unknown scenario: ${body.scenario}`);
    }
    return selected;
  }
  if (body.seed !== undefined && !Number.isSafeInteger(body.seed)) {
    throw new APIError(400, "invalid_request", "seed must be an integer");
  }
  const random = body.seed === undefined ? Math.random : mulberry32(body.seed);
  return scenarios[Math.floor(random() * scenarios.length)];
}

function mulberry32(seed) {
  let value = seed >>> 0;
  return () => {
    value += 0x6d2b79f5;
    let result = value;
    result = Math.imul(result ^ (result >>> 15), result | 1);
    result ^= result + Math.imul(result ^ (result >>> 7), result | 61);
    return ((result ^ (result >>> 14)) >>> 0) / 4294967296;
  };
}

function objectBody(body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw new APIError(400, "invalid_request", "request body must be a JSON object");
  }
  return body;
}

function rejectUnknown(body, allowed) {
  const unknown = Object.keys(body).filter((name) => !allowed.includes(name));
  if (unknown.length) {
    throw new APIError(400, "invalid_request", `unknown field(s): ${unknown.join(", ")}`);
  }
}

function mapAgentError(error) {
  if (error instanceof APIError) return error;
  if (error instanceof OllamaTimeoutError) {
    return new APIError(504, "ollama_timeout", error.message);
  }
  if (error instanceof OllamaError) {
    return new APIError(502, "ollama_error", error.message);
  }
  return error;
}

function sendError(response, status, code, message) {
  return response.status(status).json({ error: { code, message } });
}

function envNumber(name, fallback) {
  const raw = process.env[name];
  if (raw === undefined) return fallback;
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error(`${name} must be a number`);
  return value;
}

function envInteger(name, fallback) {
  const value = envNumber(name, fallback);
  if (!Number.isInteger(value)) throw new Error(`${name} must be an integer`);
  return value;
}

function validateServiceConfig(config) {
  if (config.port < 1 || config.port > 65535) {
    throw new Error("HTTP_PORT must be from 1 to 65535");
  }
  for (const [name, value] of Object.entries({
    SESSION_TTL: config.sessionTtl,
    MAX_SESSIONS: config.maxSessions,
    MAX_CONCURRENT_REQUESTS: config.maxConcurrentRequests,
    CHAT_TIMEOUT: config.chatTimeout,
    SIMULATE_TIMEOUT: config.simulateTimeout,
  })) {
    if (value <= 0) throw new Error(`${name} must be greater than zero`);
  }
}
