import { readFileSync } from "node:fs";

const catalog = JSON.parse(
  readFileSync(new URL("../data/titles.json", import.meta.url), "utf8"),
);

export const toolSchemas = [
  {
    type: "function",
    function: {
      name: "search_titles",
      description:
        "Search the bundled books, movies, and shows by medium, genre, mood, year, and approximate time commitment.",
      parameters: {
        type: "object",
        properties: {
          medium: { type: "string", enum: ["book", "movie", "show"] },
          genre: { type: "string" },
          mood: { type: "string" },
          minimum_year: { type: "integer", minimum: 1900, maximum: 2100 },
          max_time_hours: { type: "number", minimum: 0.1, maximum: 500 },
          limit: { type: "integer", minimum: 1, maximum: 10 },
        },
      },
    },
  },
  {
    type: "function",
    function: {
      name: "get_title_details",
      description: "Get full local-catalog details for one shortlisted title.",
      parameters: {
        type: "object",
        required: ["title"],
        properties: {
          title: { type: "string" },
        },
      },
    },
  },
];

export function invokeTool(name, args) {
  try {
    if (name === "search_titles") return searchTitles(args);
    if (name === "get_title_details") return getTitleDetails(args);
    return { ok: false, error: `unknown tool: ${name}` };
  } catch (error) {
    return { ok: false, error: error.message };
  }
}

function searchTitles(args) {
  rejectUnknown(args, [
    "medium",
    "genre",
    "mood",
    "minimum_year",
    "max_time_hours",
    "limit",
  ]);
  const medium = optionalString(args.medium, "medium");
  const genre = optionalString(args.genre, "genre");
  const mood = optionalString(args.mood, "mood");
  const minimumYear = optionalInteger(args.minimum_year, "minimum_year", 1900, 2100);
  const maxTime = optionalNumber(args.max_time_hours, "max_time_hours", 0.1, 500);
  const limit = optionalInteger(args.limit, "limit", 1, 10) ?? 5;

  const matches = catalog
    .filter((item) => !medium || item.medium === medium.toLowerCase())
    .filter((item) => !genre || includesFolded(item.genres, genre))
    .filter((item) => !mood || includesFolded(item.moods, mood))
    .filter((item) => minimumYear === null || item.year >= minimumYear)
    .filter((item) => maxTime === null || item.time_commitment_hours <= maxTime)
    .sort((left, right) => right.year - left.year || left.title.localeCompare(right.title))
    .slice(0, limit)
    .map((item) => ({
      title: item.title,
      medium: item.medium,
      year: item.year,
      genres: item.genres,
      moods: item.moods,
      time_commitment_hours: item.time_commitment_hours,
      length: item.length,
      summary: item.summary,
    }));
  return { ok: true, count: matches.length, titles: matches };
}

function getTitleDetails(args) {
  rejectUnknown(args, ["title"]);
  const title = requiredString(args.title, "title");
  const exact = catalog.find((item) => item.title.toLowerCase() === title.toLowerCase());
  const partial = catalog.filter((item) =>
    item.title.toLowerCase().includes(title.toLowerCase()),
  );
  const item = exact ?? (partial.length === 1 ? partial[0] : null);
  if (!item) throw new Error(`title not found or ambiguous: ${title}`);
  return {
    ok: true,
    ...item,
    notice: "Catalog metadata is static; check a local provider or bookseller for availability.",
  };
}

export function summarizeToolResult(result) {
  if (!result.ok) return `error: ${result.error ?? "unknown error"}`;
  if (result.titles) {
    return `${result.count} match(es): ${result.titles.map((item) => item.title).join(", ") || "none"}`;
  }
  if (result.title) return `details for ${result.title}`;
  return "completed";
}

function includesFolded(items, value) {
  const folded = value.toLowerCase();
  return items.some((item) => item.toLowerCase().includes(folded));
}

function requiredString(value, name) {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error(`${name} must be a non-empty string`);
  }
  return value.trim();
}

function optionalString(value, name) {
  if (value === undefined || value === null) return null;
  if (typeof value !== "string") throw new Error(`${name} must be a string`);
  return value.trim() || null;
}

function optionalInteger(value, name, minimum, maximum) {
  if (value === undefined || value === null) return null;
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error(`${name} must be an integer from ${minimum} to ${maximum}`);
  }
  return value;
}

function optionalNumber(value, name, minimum, maximum) {
  if (value === undefined || value === null) return null;
  if (typeof value !== "number" || !Number.isFinite(value) || value < minimum || value > maximum) {
    throw new Error(`${name} must be a number from ${minimum} to ${maximum}`);
  }
  return value;
}

function rejectUnknown(args, allowed) {
  const unknown = Object.keys(args).filter((name) => !allowed.includes(name));
  if (unknown.length) throw new Error(`unknown argument(s): ${unknown.join(", ")}`);
}
