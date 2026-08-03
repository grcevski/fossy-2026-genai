"""Shared configuration for the travel agent."""

from __future__ import annotations

import argparse
import os
from dataclasses import dataclass


def _env_float(name: str, default: float) -> float:
    value = os.getenv(name)
    if value is None:
        return default
    try:
        return float(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be a number") from exc


@dataclass(frozen=True)
class AgentConfig:
    ollama_host: str
    model: str
    timeout: float
    temperature: float


def add_agent_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--ollama-host",
        default=os.getenv("OLLAMA_HOST", "http://localhost:11434"),
    )
    parser.add_argument("--model", default=os.getenv("OLLAMA_MODEL", "qwen3:8b"))
    parser.add_argument(
        "--timeout", type=float, default=_env_float("OLLAMA_TIMEOUT", 120.0)
    )
    parser.add_argument(
        "--temperature",
        type=float,
        default=_env_float("OLLAMA_TEMPERATURE", 0.3),
    )


def agent_config_from_args(args: argparse.Namespace) -> AgentConfig:
    agent = AgentConfig(
        ollama_host=args.ollama_host.rstrip("/"),
        model=args.model,
        timeout=args.timeout,
        temperature=args.temperature,
    )
    validate(agent)
    return agent


def agent_config_from_env() -> AgentConfig:
    agent = AgentConfig(
        ollama_host=os.getenv("OLLAMA_HOST", "http://localhost:11434").rstrip("/"),
        model=os.getenv("OLLAMA_MODEL", "qwen3:8b"),
        timeout=_env_float("OLLAMA_TIMEOUT", 120.0),
        temperature=_env_float("OLLAMA_TEMPERATURE", 0.3),
    )
    validate(agent)
    return agent


def validate(agent: AgentConfig) -> None:
    if not agent.ollama_host.startswith(("http://", "https://")):
        raise ValueError("OLLAMA_HOST must start with http:// or https://")
    if agent.timeout <= 0:
        raise ValueError("OLLAMA_TIMEOUT must be greater than zero")
    if agent.temperature < 0:
        raise ValueError("OLLAMA_TEMPERATURE cannot be negative")
