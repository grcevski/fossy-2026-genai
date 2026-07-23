"""Local, deterministic tools exposed to the travel model."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any


DATA_PATH = Path(__file__).with_name("data") / "destinations.json"


TOOL_SCHEMAS: list[dict[str, Any]] = [
    {
        "type": "function",
        "function": {
            "name": "search_destinations",
            "description": (
                "Search the bundled destination catalog by climate, interests, "
                "travel month, and indicative daily budget in CAD."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "climate": {"type": "string"},
                    "interests": {"type": "array", "items": {"type": "string"}},
                    "travel_month": {
                        "type": "string",
                        "description": "English month name, for example September",
                    },
                    "budget_per_person_cad": {
                        "type": "number",
                        "description": "Maximum daily ground budget per person in CAD",
                    },
                    "limit": {"type": "integer", "minimum": 1, "maximum": 10},
                },
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "estimate_trip_cost",
            "description": (
                "Estimate a trip cost from bundled daily costs. All values are "
                "illustrative CAD amounts, not live prices."
            ),
            "parameters": {
                "type": "object",
                "required": ["destination", "days", "travelers"],
                "properties": {
                    "destination": {"type": "string"},
                    "days": {"type": "integer", "minimum": 1, "maximum": 90},
                    "travelers": {"type": "integer", "minimum": 1, "maximum": 20},
                    "transportation_cad": {
                        "type": "number",
                        "minimum": 0,
                        "description": "Total round-trip transportation for the party",
                    },
                },
            },
        },
    },
]


class TravelTools:
    def __init__(self) -> None:
        self.destinations: list[dict[str, Any]] = json.loads(
            DATA_PATH.read_text(encoding="utf-8")
        )

    def invoke(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        try:
            if "_invalid_arguments" in arguments:
                raise ValueError("tool arguments must be a JSON object")
            if name == "search_destinations":
                return self._search(arguments)
            if name == "estimate_trip_cost":
                return self._estimate(arguments)
            return {"ok": False, "error": f"unknown tool: {name}"}
        except (TypeError, ValueError) as exc:
            return {"ok": False, "error": str(exc)}

    def _search(self, arguments: dict[str, Any]) -> dict[str, Any]:
        _reject_unknown(
            arguments,
            {
                "climate",
                "interests",
                "travel_month",
                "budget_per_person_cad",
                "limit",
            },
        )
        climate = _optional_string(arguments, "climate")
        month = _optional_string(arguments, "travel_month")
        budget = _optional_number(arguments, "budget_per_person_cad")
        interests = _string_list(arguments.get("interests", []), "interests")
        limit = _integer(arguments.get("limit", 5), "limit", 1, 10)

        ranked: list[tuple[int, dict[str, Any]]] = []
        for destination in self.destinations:
            daily = sum(destination["daily_cost_cad"].values())
            if climate and climate.casefold() not in destination["climate"].casefold():
                continue
            if month and month.casefold() not in {
                item.casefold() for item in destination["best_months"]
            }:
                continue
            if budget is not None and daily > budget:
                continue
            available_interests = {
                item.casefold() for item in destination["interests"]
            }
            score = sum(item.casefold() in available_interests for item in interests)
            if interests and score == 0:
                continue
            ranked.append((score, destination))

        ranked.sort(key=lambda item: (-item[0], item[1]["name"]))
        matches = []
        for score, destination in ranked[:limit]:
            matches.append(
                {
                    "name": destination["name"],
                    "country": destination["country"],
                    "climate": destination["climate"],
                    "matched_interests": score,
                    "interests": destination["interests"],
                    "best_months": destination["best_months"],
                    "daily_cost_per_person_cad": sum(
                        destination["daily_cost_cad"].values()
                    ),
                    "highlights": destination["highlights"],
                    "summary": destination["summary"],
                }
            )
        return {"ok": True, "count": len(matches), "destinations": matches}

    def _estimate(self, arguments: dict[str, Any]) -> dict[str, Any]:
        _reject_unknown(
            arguments,
            {"destination", "days", "travelers", "transportation_cad"},
        )
        name = _required_string(arguments, "destination")
        days = _integer(arguments.get("days"), "days", 1, 90)
        travelers = _integer(arguments.get("travelers"), "travelers", 1, 20)
        transportation = _optional_number(arguments, "transportation_cad") or 0.0
        if transportation < 0:
            raise ValueError("transportation_cad cannot be negative")

        destination = next(
            (
                item
                for item in self.destinations
                if item["name"].casefold() == name.casefold()
            ),
            None,
        )
        if destination is None:
            raise ValueError(f"destination not found: {name}")

        breakdown = {
            category: round(amount * days * travelers, 2)
            for category, amount in destination["daily_cost_cad"].items()
        }
        breakdown["round_trip_transportation"] = round(transportation, 2)
        return {
            "ok": True,
            "destination": destination["name"],
            "currency": "CAD",
            "days": days,
            "travelers": travelers,
            "breakdown_cad": breakdown,
            "total_cad": round(sum(breakdown.values()), 2),
            "notice": "Illustrative catalog estimate; verify current prices.",
        }


def summarize_result(result: dict[str, Any]) -> str:
    if not result.get("ok"):
        return f"error: {result.get('error', 'unknown error')}"
    if "destinations" in result:
        names = ", ".join(item["name"] for item in result["destinations"])
        return f"{result['count']} match(es): {names or 'none'}"
    if "total_cad" in result:
        return f"estimated total CAD {result['total_cad']:,.2f}"
    return "completed"


def _required_string(arguments: dict[str, Any], key: str) -> str:
    value = arguments.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{key} must be a non-empty string")
    return value.strip()


def _optional_string(arguments: dict[str, Any], key: str) -> str | None:
    value = arguments.get(key)
    if value is None:
        return None
    if not isinstance(value, str):
        raise ValueError(f"{key} must be a string")
    return value.strip() or None


def _optional_number(arguments: dict[str, Any], key: str) -> float | None:
    value = arguments.get(key)
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{key} must be a number")
    return float(value)


def _integer(value: Any, key: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{key} must be an integer")
    if int(value) != value or not minimum <= int(value) <= maximum:
        raise ValueError(f"{key} must be an integer from {minimum} to {maximum}")
    return int(value)


def _string_list(value: Any, key: str) -> list[str]:
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise ValueError(f"{key} must be a list of strings")
    return [item.strip() for item in value if item.strip()]


def _reject_unknown(arguments: dict[str, Any], allowed: set[str]) -> None:
    unknown = sorted(set(arguments) - allowed)
    if unknown:
        raise ValueError(f"unknown argument(s): {', '.join(unknown)}")
