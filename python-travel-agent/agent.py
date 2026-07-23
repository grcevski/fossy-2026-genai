"""Ollama chat loop for the Python travel agent."""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from collections.abc import Callable
from typing import Any

from config import AgentConfig
from tools import TOOL_SCHEMAS, TravelTools


SYSTEM_PROMPT = """You are a friendly offline travel-planning agent.
Use the supplied tools whenever you recommend a destination or estimate a budget.
The catalog is static demo data. Clearly label costs as illustrative CAD estimates and
never claim current prices, availability, visa rules, safety conditions, or professional
advice. Ask at most one focused clarification when missing information would materially
change the result; otherwise state reasonable assumptions. After tool use, give concise,
practical recommendations and a possible itinerary. Never invent tool results."""

MAX_TOOL_ROUNDS = 5
MAX_HISTORY_TURNS = 10

ToolCallback = Callable[[str, dict[str, Any], dict[str, Any]], None]


class OllamaError(RuntimeError):
    pass


class OllamaClient:
    def __init__(self, config: AgentConfig) -> None:
        self.config = config

    def chat(self, messages: list[dict[str, Any]]) -> dict[str, Any]:
        payload = {
            "model": self.config.model,
            "messages": messages,
            "tools": TOOL_SCHEMAS,
            "stream": False,
            "think": False,
            "options": {"temperature": self.config.temperature},
        }
        request = urllib.request.Request(
            f"{self.config.ollama_host}/api/chat",
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self.config.timeout) as response:
                body = response.read().decode("utf-8")
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise OllamaError(f"Ollama returned HTTP {exc.code}: {detail}") from exc
        except (urllib.error.URLError, TimeoutError) as exc:
            raise OllamaError(f"cannot reach Ollama: {exc}") from exc
        try:
            result = json.loads(body)
            message = result["message"]
        except (json.JSONDecodeError, KeyError, TypeError) as exc:
            raise OllamaError("Ollama returned an invalid chat response") from exc
        if not isinstance(message, dict):
            raise OllamaError("Ollama response message is not an object")
        return message


class TravelAgent:
    def __init__(self, config: AgentConfig) -> None:
        self.client = OllamaClient(config)
        self.tools = TravelTools()
        self.turns: list[list[dict[str, Any]]] = []

    def reset(self) -> None:
        self.turns.clear()

    def ask(self, user_text: str, on_tool: ToolCallback | None = None) -> str:
        current: list[dict[str, Any]] = [{"role": "user", "content": user_text}]

        for _ in range(MAX_TOOL_ROUNDS):
            messages = [{"role": "system", "content": SYSTEM_PROMPT}]
            messages.extend(message for turn in self.turns for message in turn)
            messages.extend(current)
            response = self.client.chat(messages)
            assistant = _assistant_message(response)
            current.append(assistant)

            tool_calls = assistant.get("tool_calls", [])
            if not tool_calls:
                answer = assistant.get("content", "").strip()
                if not answer:
                    answer = "I could not produce a response for that request."
                self._remember(current)
                return answer

            for call in tool_calls:
                name, arguments = _parse_tool_call(call)
                result = self.tools.invoke(name, arguments)
                if on_tool:
                    on_tool(name, arguments, result)
                current.append(
                    {
                        "role": "tool",
                        "tool_name": name,
                        "content": json.dumps(result, ensure_ascii=False),
                    }
                )

        answer = "I stopped after five tool rounds to avoid an accidental loop."
        current.append({"role": "assistant", "content": answer})
        self._remember(current)
        return answer

    def _remember(self, turn: list[dict[str, Any]]) -> None:
        self.turns.append(turn)
        self.turns = self.turns[-MAX_HISTORY_TURNS:]


def _assistant_message(response: dict[str, Any]) -> dict[str, Any]:
    message: dict[str, Any] = {
        "role": "assistant",
        "content": response.get("content", ""),
    }
    if response.get("tool_calls"):
        message["tool_calls"] = response["tool_calls"]
    return message


def _parse_tool_call(call: Any) -> tuple[str, dict[str, Any]]:
    if not isinstance(call, dict) or not isinstance(call.get("function"), dict):
        return "invalid_tool_call", {}
    function = call["function"]
    name = function.get("name")
    if not isinstance(name, str) or not name:
        return "invalid_tool_call", {}
    arguments = function.get("arguments", {})
    if isinstance(arguments, str):
        try:
            arguments = json.loads(arguments)
        except json.JSONDecodeError:
            return name, {"_invalid_arguments": arguments}
    if not isinstance(arguments, dict):
        return name, {"_invalid_arguments": arguments}
    return name, arguments

