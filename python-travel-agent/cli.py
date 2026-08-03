#!/usr/bin/env python3
"""Interactive entry point for the travel agent."""

from __future__ import annotations

import argparse
import json
import os
import sys

from agent import OllamaError, TravelAgent
from config import add_agent_arguments, agent_config_from_args
from tools import summarize_result


def main() -> int:
    parser = argparse.ArgumentParser(description="Interactive offline travel agent")
    add_agent_arguments(parser)
    try:
        agent_config = agent_config_from_args(parser.parse_args())
    except ValueError as exc:
        parser.error(str(exc))

    color = sys.stdout.isatty() and "NO_COLOR" not in os.environ
    cyan, green, yellow, reset = ("\033[36m", "\033[32m", "\033[33m", "\033[0m") if color else ("", "", "", "")
    agent = TravelAgent(agent_config)

    print("Python travel agent — static demo catalog; CAD estimates are illustrative.")
    print("Commands: /help, /reset, /quit")
    while True:
        try:
            text = input(f"{cyan}you>{reset} ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\nGoodbye.")
            return 0
        if not text:
            continue
        if text == "/quit":
            print("Goodbye.")
            return 0
        if text == "/help":
            print("Ask for destinations, budgets, or itineraries. Use /reset to forget this session.")
            continue
        if text == "/reset":
            agent.reset()
            print("Conversation reset.")
            continue

        def show_tool(name: str, arguments: dict, result: dict) -> None:
            compact_args = json.dumps(arguments, ensure_ascii=False, separators=(",", ":"))
            print(f"{yellow}[tool]{reset} {name} {compact_args} -> {summarize_result(result)}")

        try:
            answer = agent.ask(text, show_tool)
            print(f"{green}agent>{reset} {answer}")
        except OllamaError as exc:
            print(f"error: {exc}", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
