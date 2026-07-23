#!/usr/bin/env node
import { createInterface } from "node:readline/promises";
import { stdin as input, stdout as output } from "node:process";

import { EntertainmentAgent, OllamaError } from "./agent.js";
import { loadConfig, printAgentHelp } from "./config.js";
import { summarizeToolResult } from "./tools.js";

async function main() {
  let parsed;
  try {
    parsed = loadConfig(process.argv.slice(2));
  } catch (error) {
    console.error(`error: ${error.message}`);
    process.exitCode = 2;
    return;
  }
  if (parsed.help) {
    printAgentHelp("node src/cli.js");
    return;
  }

  const color = Boolean(output.isTTY && !process.env.NO_COLOR);
  const cyan = color ? "\x1b[36m" : "";
  const green = color ? "\x1b[32m" : "";
  const yellow = color ? "\x1b[33m" : "";
  const reset = color ? "\x1b[0m" : "";
  const agent = new EntertainmentAgent(parsed.config);
  const terminal = createInterface({ input, output });

  console.log("Node.js entertainment agent — static demo catalog; check local availability.");
  console.log("Commands: /help, /reset, /quit");
  try {
    while (true) {
      let text;
      try {
        text = (await terminal.question(`${cyan}you>${reset} `)).trim();
      } catch {
        console.log("\nGoodbye.");
        break;
      }
      if (!text) continue;
      if (text === "/quit") {
        console.log("Goodbye.");
        break;
      }
      if (text === "/help") {
        console.log("Ask for books, movies, shows, comparisons, moods, genres, or time limits.");
        continue;
      }
      if (text === "/reset") {
        agent.reset();
        console.log("Conversation reset.");
        continue;
      }
      try {
        const answer = await agent.ask(text, (name, args, result) => {
          console.log(
            `${yellow}[tool]${reset} ${name} ${JSON.stringify(args)} -> ${summarizeToolResult(result)}`,
          );
        });
        console.log(`${green}agent>${reset} ${answer}`);
      } catch (error) {
        if (error instanceof OllamaError) console.error(`error: ${error.message}`);
        else throw error;
      }
    }
  } finally {
    terminal.close();
  }
}

await main();

