"""FastAPI service for the travel agent."""

from __future__ import annotations

import json
import os
import random
import urllib.request
import uuid
from dataclasses import dataclass
from pathlib import Path
from time import monotonic
from typing import Any

from fastapi import Body, Depends, FastAPI, Request, Response
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict
from starlette.exceptions import HTTPException as StarletteHTTPException

from agent import OllamaError, OllamaTimeoutError, TravelAgent
from config import AgentConfig, agent_config_from_env
from tools import summarize_result


SERVICE_NAME = "travel"
MAX_BODY_BYTES = 32 * 1024
MAX_MESSAGE_CHARS = 8_000
SESSION_TTL_SECONDS = float(os.getenv("SESSION_TTL", "1800"))
MAX_SESSIONS = int(os.getenv("MAX_SESSIONS", "100"))
CHAT_TIMEOUT_SECONDS = float(os.getenv("CHAT_TIMEOUT", "300"))
SIMULATE_TIMEOUT_SECONDS = float(os.getenv("SIMULATE_TIMEOUT", "900"))
SCENARIOS_PATH = Path(__file__).with_name("data") / "scenarios.json"


class APIError(Exception):
    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message


class ChatRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    message: str
    session_id: str | None = None


class SimulationRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    scenario: str | None = None
    seed: int | None = None


@dataclass
class Session:
    agent: TravelAgent
    last_used: float


class SessionStore:
    def __init__(self, agent_config: AgentConfig) -> None:
        self.agent_config = agent_config
        self.sessions: dict[str, Session] = {}

    def acquire(self, session_id: str | None) -> tuple[str, Session]:
        now = monotonic()
        self._expire(now)
        if session_id is not None:
            session = self.sessions.get(session_id)
            if session is None:
                raise APIError(404, "session_not_found", "unknown session_id")
            session.last_used = monotonic()
            return session_id, session

        if len(self.sessions) >= MAX_SESSIONS:
            oldest_id = min(
                self.sessions, key=lambda identifier: self.sessions[identifier].last_used
            )
            del self.sessions[oldest_id]

        identifier = str(uuid.uuid4())
        session = Session(agent=TravelAgent(self.agent_config), last_used=now)
        self.sessions[identifier] = session
        return identifier, session

    def delete(self, session_id: str) -> None:
        self.sessions.pop(session_id, None)

    def _expire(self, now: float) -> None:
        expired = [
            identifier
            for identifier, session in self.sessions.items()
            if now - session.last_used > SESSION_TTL_SECONDS
        ]
        for identifier in expired:
            del self.sessions[identifier]


def _positive_configuration() -> None:
    for value, name in (
        (SESSION_TTL_SECONDS, "SESSION_TTL"),
        (MAX_SESSIONS, "MAX_SESSIONS"),
        (CHAT_TIMEOUT_SECONDS, "CHAT_TIMEOUT"),
        (SIMULATE_TIMEOUT_SECONDS, "SIMULATE_TIMEOUT"),
    ):
        if value <= 0:
            raise RuntimeError(f"{name} must be greater than zero")


_positive_configuration()
agent_config = agent_config_from_env()
session_store = SessionStore(agent_config)
scenarios: list[dict[str, Any]] = json.loads(SCENARIOS_PATH.read_text(encoding="utf-8"))
app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)


def error_response(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse(
        status_code=status,
        content={"error": {"code": code, "message": message}},
    )


async def enforce_body_limit(request: Request) -> None:
    value = request.headers.get("content-length")
    if value is None:
        if request.headers.get("transfer-encoding"):
            raise APIError(400, "invalid_request", "chunked request bodies are not supported")
        return
    try:
        size = int(value)
    except ValueError as exc:
        raise APIError(400, "invalid_request", "invalid Content-Length") from exc
    if size < 0 or size > MAX_BODY_BYTES:
        raise APIError(400, "invalid_request", "request body exceeds 32 KiB")


@app.exception_handler(APIError)
async def handle_api_error(_request: Request, exc: APIError) -> JSONResponse:
    return error_response(exc.status, exc.code, exc.message)


@app.exception_handler(StarletteHTTPException)
async def handle_http_error(
    _request: Request, exc: StarletteHTTPException
) -> JSONResponse:
    code = "not_found" if exc.status_code == 404 else "method_not_allowed"
    return error_response(exc.status_code, code, str(exc.detail))


@app.exception_handler(RequestValidationError)
async def handle_validation_error(
    _request: Request, exc: RequestValidationError
) -> JSONResponse:
    if any(error.get("type") == "json_invalid" for error in exc.errors()):
        return error_response(400, "invalid_json", "request body must be valid JSON")
    return error_response(400, "invalid_request", "invalid request")


@app.exception_handler(Exception)
async def handle_unexpected_error(_request: Request, _exc: Exception) -> JSONResponse:
    return error_response(500, "internal_error", "unexpected service error")


@app.get("/healthz")
async def health() -> dict[str, str]:
    return {"status": "ok", "service": SERVICE_NAME}


@app.get("/readyz")
async def ready() -> dict[str, str]:
    try:
        available = _ollama_models()
    except Exception as exc:
        raise APIError(503, "not_ready", "Ollama is unavailable") from exc
    if agent_config.model not in available:
        raise APIError(503, "not_ready", f"model {agent_config.model} is unavailable")
    return {"status": "ready", "service": SERVICE_NAME, "model": agent_config.model}


@app.post("/v1/chat", dependencies=[Depends(enforce_body_limit)])
async def chat(body: ChatRequest) -> dict[str, Any]:
    deadline = monotonic() + CHAT_TIMEOUT_SECONDS
    message = body.message.strip()
    if not message:
        raise APIError(400, "invalid_request", "message must be a non-empty string")
    if len(message) > MAX_MESSAGE_CHARS:
        raise APIError(400, "invalid_request", "message exceeds 8000 characters")
    session_id = body.session_id.strip() if body.session_id is not None else None
    if session_id == "":
        raise APIError(400, "invalid_request", "session_id must be a non-empty string")

    identifier, session = session_store.acquire(session_id)
    traces: list[dict[str, Any]] = []

    def on_tool(name: str, arguments: dict, result: dict) -> None:
        traces.append(
            {
                "name": name,
                "arguments": arguments,
                "summary": summarize_result(result),
            }
        )

    try:
        answer = session.agent.ask(message, on_tool, deadline)
    except OllamaTimeoutError as exc:
        raise APIError(504, "ollama_timeout", str(exc)) from exc
    except OllamaError as exc:
        raise APIError(502, "ollama_error", str(exc)) from exc
    session.last_used = monotonic()
    return {"session_id": identifier, "answer": answer, "tools": traces}


@app.post("/v1/simulate", dependencies=[Depends(enforce_body_limit)])
async def simulate(
    body: SimulationRequest | None = Body(default=None),
) -> dict[str, Any]:
    selected = _select_scenario(body or SimulationRequest())

    deadline = monotonic() + SIMULATE_TIMEOUT_SECONDS
    try:
        transcript = _run_scenario(selected, deadline)
    except OllamaTimeoutError as exc:
        raise APIError(504, "ollama_timeout", str(exc)) from exc
    except OllamaError as exc:
        raise APIError(502, "ollama_error", str(exc)) from exc
    return {"scenario": selected["name"], "transcript": transcript}


@app.delete("/v1/sessions/{session_id}", status_code=204)
async def delete_session(session_id: str) -> Response:
    session_store.delete(session_id)
    return Response(status_code=204)


def _select_scenario(body: SimulationRequest) -> dict[str, Any]:
    name = body.scenario
    if name is not None:
        name = name.strip()
        if not name:
            raise APIError(400, "invalid_request", "scenario must be a non-empty string")
        selected = next((item for item in scenarios if item["name"] == name), None)
        if selected is None:
            raise APIError(400, "unknown_scenario", f"unknown scenario: {name}")
        return selected
    seed = body.seed
    chooser: random.Random = (
        random.Random(seed) if seed is not None else random.SystemRandom()
    )
    return chooser.choice(scenarios)


def _run_scenario(selected: dict[str, Any], deadline: float) -> list[dict[str, Any]]:
    simulation_agent = TravelAgent(agent_config)
    transcript: list[dict[str, Any]] = []
    for prompt in selected["prompts"]:
        traces: list[dict[str, Any]] = []

        def on_tool(name: str, arguments: dict, result: dict) -> None:
            traces.append(
                {
                    "name": name,
                    "arguments": arguments,
                    "summary": summarize_result(result),
                }
            )

        answer = simulation_agent.ask(prompt, on_tool, deadline)
        transcript.append({"message": prompt, "answer": answer, "tools": traces})
    return transcript


def _ollama_models() -> set[str]:
    request = urllib.request.Request(f"{agent_config.ollama_host}/api/tags")
    with urllib.request.urlopen(request, timeout=min(agent_config.timeout, 5)) as response:
        payload = json.loads(response.read().decode("utf-8"))
    return {
        value
        for model in payload.get("models", [])
        for value in (model.get("name"), model.get("model"))
        if isinstance(value, str)
    }


def main() -> None:
    import uvicorn

    port = int(os.getenv("HTTP_PORT", "8081"))
    if port < 1 or port > 65535:
        raise RuntimeError("HTTP_PORT must be from 1 to 65535")
    uvicorn.run(
        app,
        host=os.getenv("HTTP_HOST", "0.0.0.0"),
        port=port,
        timeout_graceful_shutdown=10,
    )


if __name__ == "__main__":
    main()
