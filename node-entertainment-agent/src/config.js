function envNumber(name, fallback) {
  const raw = process.env[name];
  if (raw === undefined) return fallback;
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error(`${name} must be a number`);
  return value;
}

export function loadConfig(argv) {
  const config = {
    ollamaHost: process.env.OLLAMA_HOST ?? "http://localhost:11434",
    model: process.env.OLLAMA_MODEL ?? "qwen3:8b",
    timeout: envNumber("OLLAMA_TIMEOUT", 120),
    temperature: envNumber("OLLAMA_TEMPERATURE", 0.3),
  };
  const values = parseArguments(argv);
  if (values.help) return { help: true, config };
  apply(values, config);
  validate(config);
  config.ollamaHost = config.ollamaHost.replace(/\/$/, "");
  return { help: false, config };
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

function apply(values, config) {
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
  for (const name of Object.keys(values)) {
    if (!known.has(name)) throw new Error(`unknown option: --${name}`);
  }
}

function flagNumber(raw, name) {
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error(`--${name} must be a number`);
  return value;
}

function validate(config) {
  if (!/^https?:\/\//.test(config.ollamaHost)) {
    throw new Error("OLLAMA_HOST must start with http:// or https://");
  }
  if (config.timeout <= 0) throw new Error("OLLAMA_TIMEOUT must be greater than zero");
  if (config.temperature < 0) throw new Error("OLLAMA_TEMPERATURE cannot be negative");
}

export function printAgentHelp(program) {
  console.log(`Usage: ${program} [options]

  --ollama-host URL       Ollama base URL
  --model NAME            Ollama model
  --timeout SECONDS       HTTP request timeout
  --temperature NUMBER    Sampling temperature
  -h, --help              Show this help`);
}
