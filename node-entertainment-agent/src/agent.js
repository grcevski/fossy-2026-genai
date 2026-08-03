import { invokeTool, toolSchemas } from "./tools.js";

const systemPrompt = `You are a friendly offline entertainment recommendation agent.
Use the supplied tools whenever you recommend or compare books, movies, or shows. The
catalog and metadata are static demo data. Do not claim current streaming, store, or
library availability; tell users to check their local providers. Ask at most one focused
clarification when a missing preference would materially change the result; otherwise
state reasonable assumptions. Explain why each recommendation fits and never invent
tool results.`;

const maxToolRounds = 5;
const maxHistoryTurns = 10;

export class OllamaError extends Error {}
export class OllamaTimeoutError extends OllamaError {}

export class EntertainmentAgent {
  constructor(config) {
    this.config = config;
    this.turns = [];
  }

  reset() {
    this.turns = [];
  }

  async ask(userText, onTool = null, deadline = null) {
    const current = [{ role: "user", content: userText }];
    for (let round = 0; round < maxToolRounds; round += 1) {
      const messages = [
        { role: "system", content: systemPrompt },
        ...this.turns.flat(),
        ...current,
      ];
      const response = await this.#chat(messages, deadline);
      const assistant = {
        role: "assistant",
        content: response.content ?? "",
        ...(response.tool_calls?.length ? { tool_calls: response.tool_calls } : {}),
      };
      current.push(assistant);
      if (!assistant.tool_calls?.length) {
        const answer =
          assistant.content.trim() || "I could not produce a response for that request.";
        this.#remember(current);
        return answer;
      }

      for (const call of assistant.tool_calls) {
        const { name, arguments: args } = parseToolCall(call);
        const result = invokeTool(name, args);
        if (onTool) onTool(name, args, result);
        current.push({
          role: "tool",
          tool_name: name,
          content: JSON.stringify(result),
        });
      }
    }
    const answer = "I stopped after five tool rounds to avoid an accidental loop.";
    current.push({ role: "assistant", content: answer });
    this.#remember(current);
    return answer;
  }

  #remember(turn) {
    this.turns.push(turn);
    this.turns = this.turns.slice(-maxHistoryTurns);
  }

  async #chat(messages, deadline) {
    const remaining = deadline === null
      ? this.config.timeout * 1000
      : Math.min(this.config.timeout * 1000, deadline - Date.now());
    if (remaining <= 0) throw new OllamaTimeoutError("request deadline exceeded");
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), remaining);
    let response;
    try {
      response = await fetch(`${this.config.ollamaHost}/api/chat`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          model: this.config.model,
          messages,
          tools: toolSchemas,
          stream: false,
          think: false,
          options: { temperature: this.config.temperature },
        }),
        signal: controller.signal,
      });
    } catch (error) {
      const reason = error.name === "AbortError" ? "request timed out" : error.message;
      if (error.name === "AbortError") throw new OllamaTimeoutError(reason);
      throw new OllamaError(`cannot reach Ollama: ${reason}`);
    } finally {
      clearTimeout(timeout);
    }

    const body = await response.text();
    if (!response.ok) {
      throw new OllamaError(`Ollama returned HTTP ${response.status}: ${body}`);
    }
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch {
      throw new OllamaError("Ollama returned invalid JSON");
    }
    if (!parsed.message || typeof parsed.message !== "object") {
      throw new OllamaError("Ollama returned an invalid chat response");
    }
    return parsed.message;
  }
}

function parseToolCall(call) {
  const name = call?.function?.name;
  let args = call?.function?.arguments ?? {};
  if (typeof args === "string") {
    try {
      args = JSON.parse(args);
    } catch {
      args = { _invalid_arguments: args };
    }
  }
  return {
    name: typeof name === "string" && name ? name : "invalid_tool_call",
    arguments: args && typeof args === "object" && !Array.isArray(args) ? args : {},
  };
}
