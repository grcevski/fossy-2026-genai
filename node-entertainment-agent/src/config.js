function envNumber(name, fallback) {
  const raw = process.env[name];
  if (raw === undefined) return fallback;
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error(`${name} must be a number`);
  return value;
}

function envInteger(name, fallback) {
  const value = envNumber(name, fallback);
  if (value !== null && !Number.isInteger(value)) {
    throw new Error(`${name} must be an integer`);
  }
  return value;
}

export function loadConfig(argv, includeDriver = false) {
  const config = {
    ollamaHost: process.env.OLLAMA_HOST ?? "http://localhost:11434",
    model: process.env.OLLAMA_MODEL ?? "qwen3:8b",
    timeout: envNumber("OLLAMA_TIMEOUT", 120),
    temperature: envNumber("OLLAMA_TEMPERATURE", 0.3),
  };
  const driver = includeDriver
    ? {
        workers: envInteger("WORKERS", 1),
        minDelay: envNumber("MIN_DELAY", 2),
        maxDelay: envNumber("MAX_DELAY", 8),
        sessionMinDelay: envNumber("SESSION_MIN_DELAY", 5),
        sessionMaxDelay: envNumber("SESSION_MAX_DELAY", 15),
        randomSeed:
          process.env.RANDOM_SEED === undefined
            ? null
            : envInteger("RANDOM_SEED", null),
        maxSessions:
          process.env.MAX_SESSIONS === undefined
            ? null
            : envInteger("MAX_SESSIONS", null),
      }
    : null;

  const values = parseArguments(argv);
  if (values.help) return { help: true, config, driver };
  apply(values, config, driver);
  validate(config, driver);
  config.ollamaHost = config.ollamaHost.replace(/\/$/, "");
  return { help: false, config, driver };
}

function parseArguments(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--help" || argument === "-h") {
      values.help = true;
      continue;
    }
    if (!argument.startsWith("--")) throw new Error(`unexpected argument: ${argument}`);
    const equals = argument.indexOf("=");
    let name;
    let value;
    if (equals >= 0) {
      name = argument.slice(2, equals);
      value = argument.slice(equals + 1);
    } else {
      name = argument.slice(2);
      value = argv[index + 1];
      if (value === undefined || value.startsWith("--")) {
        throw new Error(`missing value for --${name}`);
      }
      index += 1;
    }
    values[name] = value;
  }
  return values;
}

function apply(values, config, driver) {
  const known = new Set([
    "help",
    "ollama-host",
    "model",
    "timeout",
    "temperature",
  ]);
  if (values["ollama-host"] !== undefined) config.ollamaHost = values["ollama-host"];
  if (values.model !== undefined) config.model = values.model;
  if (values.timeout !== undefined) config.timeout = flagNumber(values.timeout, "timeout");
  if (values.temperature !== undefined) {
    config.temperature = flagNumber(values.temperature, "temperature");
  }
  if (driver) {
    for (const name of [
      "workers",
      "min-delay",
      "max-delay",
      "session-min-delay",
      "session-max-delay",
      "random-seed",
      "max-sessions",
    ]) {
      known.add(name);
    }
    if (values.workers !== undefined) driver.workers = flagInteger(values.workers, "workers");
    if (values["min-delay"] !== undefined) driver.minDelay = flagNumber(values["min-delay"], "min-delay");
    if (values["max-delay"] !== undefined) driver.maxDelay = flagNumber(values["max-delay"], "max-delay");
    if (values["session-min-delay"] !== undefined) driver.sessionMinDelay = flagNumber(values["session-min-delay"], "session-min-delay");
    if (values["session-max-delay"] !== undefined) driver.sessionMaxDelay = flagNumber(values["session-max-delay"], "session-max-delay");
    if (values["random-seed"] !== undefined) driver.randomSeed = flagInteger(values["random-seed"], "random-seed");
    if (values["max-sessions"] !== undefined) driver.maxSessions = flagInteger(values["max-sessions"], "max-sessions");
  }
  for (const name of Object.keys(values)) {
    if (!known.has(name)) throw new Error(`unknown option: --${name}`);
  }
}

function flagNumber(raw, name) {
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error(`--${name} must be a number`);
  return value;
}

function flagInteger(raw, name) {
  const value = flagNumber(raw, name);
  if (!Number.isInteger(value)) throw new Error(`--${name} must be an integer`);
  return value;
}

function validate(config, driver) {
  if (!/^https?:\/\//.test(config.ollamaHost)) {
    throw new Error("OLLAMA_HOST must start with http:// or https://");
  }
  if (config.timeout <= 0) throw new Error("OLLAMA_TIMEOUT must be greater than zero");
  if (config.temperature < 0) throw new Error("OLLAMA_TEMPERATURE cannot be negative");
  if (!driver) return;
  if (driver.workers < 1) throw new Error("WORKERS must be at least 1");
  if (driver.maxSessions !== null && driver.maxSessions < 1) {
    throw new Error("MAX_SESSIONS must be at least 1");
  }
  if (driver.minDelay < 0 || driver.maxDelay < driver.minDelay) {
    throw new Error("invalid prompt delay range");
  }
  if (
    driver.sessionMinDelay < 0 ||
    driver.sessionMaxDelay < driver.sessionMinDelay
  ) {
    throw new Error("invalid session delay range");
  }
}

export function printAgentHelp(program) {
  console.log(`Usage: ${program} [options]

  --ollama-host URL       Ollama base URL
  --model NAME            Ollama model
  --timeout SECONDS       HTTP request timeout
  --temperature NUMBER    Sampling temperature
  -h, --help              Show this help`);
}

export function printDriverHelp(program) {
  printAgentHelp(program);
  console.log(`
Driver options:
  --workers NUMBER
  --min-delay SECONDS
  --max-delay SECONDS
  --session-min-delay SECONDS
  --session-max-delay SECONDS
  --random-seed NUMBER
  --max-sessions NUMBER`);
}

