"""Shared configuration for the travel CLI and workload driver."""

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


def _env_int(name: str, default: int | None) -> int | None:
    value = os.getenv(name)
    if value is None:
        return default
    try:
        return int(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be an integer") from exc


@dataclass(frozen=True)
class AgentConfig:
    ollama_host: str
    model: str
    timeout: float
    temperature: float


@dataclass(frozen=True)
class DriverConfig:
    workers: int
    min_delay: float
    max_delay: float
    session_min_delay: float
    session_max_delay: float
    random_seed: int | None
    max_sessions: int | None


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


def add_driver_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--workers", type=int, default=_env_int("WORKERS", 1))
    parser.add_argument(
        "--min-delay", type=float, default=_env_float("MIN_DELAY", 2.0)
    )
    parser.add_argument(
        "--max-delay", type=float, default=_env_float("MAX_DELAY", 8.0)
    )
    parser.add_argument(
        "--session-min-delay",
        type=float,
        default=_env_float("SESSION_MIN_DELAY", 5.0),
    )
    parser.add_argument(
        "--session-max-delay",
        type=float,
        default=_env_float("SESSION_MAX_DELAY", 15.0),
    )
    parser.add_argument(
        "--random-seed", type=int, default=_env_int("RANDOM_SEED", None)
    )
    parser.add_argument(
        "--max-sessions", type=int, default=_env_int("MAX_SESSIONS", None)
    )


def configs_from_args(args: argparse.Namespace) -> tuple[AgentConfig, DriverConfig | None]:
    agent = AgentConfig(
        ollama_host=args.ollama_host.rstrip("/"),
        model=args.model,
        timeout=args.timeout,
        temperature=args.temperature,
    )
    driver = None
    if hasattr(args, "workers"):
        driver = DriverConfig(
            workers=args.workers,
            min_delay=args.min_delay,
            max_delay=args.max_delay,
            session_min_delay=args.session_min_delay,
            session_max_delay=args.session_max_delay,
            random_seed=args.random_seed,
            max_sessions=args.max_sessions,
        )
    validate(agent, driver)
    return agent, driver


def validate(agent: AgentConfig, driver: DriverConfig | None) -> None:
    if not agent.ollama_host.startswith(("http://", "https://")):
        raise ValueError("OLLAMA_HOST must start with http:// or https://")
    if agent.timeout <= 0:
        raise ValueError("OLLAMA_TIMEOUT must be greater than zero")
    if agent.temperature < 0:
        raise ValueError("OLLAMA_TEMPERATURE cannot be negative")
    if driver is None:
        return
    if driver.workers < 1:
        raise ValueError("WORKERS must be at least 1")
    if driver.max_sessions is not None and driver.max_sessions < 1:
        raise ValueError("MAX_SESSIONS must be at least 1")
    for minimum, maximum, label in (
        (driver.min_delay, driver.max_delay, "prompt delay"),
        (driver.session_min_delay, driver.session_max_delay, "session delay"),
    ):
        if minimum < 0 or maximum < minimum:
            raise ValueError(f"invalid {label} range")

