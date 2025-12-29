#!/usr/bin/env bun
/// <reference types="bun-types" />
/**
 * llm-mux test via OpenCode SDK
 * Multi-turn conversation with tool usage
 */
import { createOpencode, type OpencodeClient } from "@opencode-ai/sdk";

const LLM_MUX_URL = "http://localhost:8318";

// Provider configs for different API formats
const providers = {
  google: {
    npm: "@ai-sdk/google",
    name: "LLM-Mux",
    options: { baseURL: `${LLM_MUX_URL}/v1beta`, apiKey: "x" },
  },
  openai: {
    npm: "@ai-sdk/openai",
    name: "LLM-Mux",
    options: { baseURL: `${LLM_MUX_URL}/v1`, apiKey: "x" },
  },
  anthropic: {
    npm: "@ai-sdk/anthropic",
    name: "LLM-Mux",
    options: { baseURL: `${LLM_MUX_URL}/v1`, apiKey: "x" },
  },
} as const;

// Select provider: google | openai | anthropic
const activeProvider = providers.anthropic;

type Client = OpencodeClient;

interface PromptResponse {
  parts?: Array<Record<string, unknown>>;
  info: { error?: string };
}

// Display response parts
function displayParts(parts: Array<Record<string, unknown>> = []) {
  for (const p of parts) {
    switch (p.type) {
      case "reasoning":
        console.log("\x1b[2m[Reasoning]\x1b[0m");
        console.log(`\x1b[2m${p.text}\x1b[0m\n`);
        break;
      case "text":
        console.log("[Response]");
        console.log(p.text);
        break;
      case "tool": {
        const state = p.state as Record<string, unknown>;
        console.log(`\x1b[33m[Tool: ${p.tool}]\x1b[0m ${state.status}`);
        if (state.input) console.log("  Input:", JSON.stringify(state.input));
        if (state.output) {
          const out = String(state.output);
          console.log(
            "  Output:",
            out.length > 200 ? `${out.slice(0, 200)}...` : out,
          );
        }
        if (state.error) console.log("  Error:", state.error);
        break;
      }
    }
  }
}

// Send prompt and display response
async function prompt(
  client: Client,
  sessionId: string,
  text: string,
  label: string,
): Promise<PromptResponse | null> {
  console.log(`\n${"=".repeat(60)}`);
  console.log(`\x1b[36m[Turn ${label}]\x1b[0m ${text}`);
  console.log("=".repeat(60));

  const { data } = await client.session.prompt({
    path: { id: sessionId },
    body: {
      model: { providerID: "llm-mux", modelID: "claude-thinking" },
      parts: [{ type: "text", text }],
    },
  });

  if (data?.info.error) {
    console.error("\x1b[31mError:\x1b[0m", data.info.error);
    return null;
  }

  displayParts(data?.parts as Array<Record<string, unknown>>);
  return data as PromptResponse;
}

// Main test
async function main() {
  const { client, server } = await createOpencode({
    port: 10000 + Math.floor(Math.random() * 50000),
    config: {
      model: "llm-mux/claude-thinking",
      provider: {
        "llm-mux": {
          ...activeProvider,
          models: {
            "claude-thinking": {
              id: "claude-sonnet-4-5-thinking",
              name: "Claude Sonnet Thinking",
              tool_call: true,
              reasoning: true,
              options: {
                maxOutputTokens: 64000,
                thinking: { type: "enabled", budgetTokens: 8000 },
              },
            },
          },
        },
      },
      tools: { bash: true, read: true, glob: true, write: true },
    },
  });

  try {
    const { data: session } = await client.session.create({
      body: { title: "llm-mux-multiturn-test" },
    });
    if (!session?.id) throw new Error("Failed to create session");

    console.log(`\x1b[32mSession:\x1b[0m ${session.id}`);

    // Turn 1: Simple reasoning test
    await prompt(
      client,
      session.id,
      "What is 17 * 23? Think step by step.",
      "1",
    );

    // Turn 2: Tool usage - read a file
    await prompt(
      client,
      session.id,
      "Read the file go.mod in this project and tell me the Go version.",
      "2",
    );

    // Turn 3: Follow-up referencing previous context
    await prompt(
      client,
      session.id,
      "Based on the go.mod you just read, list 3 main dependencies.",
      "3",
    );

    // Turn 4: Another tool - glob search
    await prompt(
      client,
      session.id,
      "Use glob to find all .go files in the internal/config directory.",
      "4",
    );

    // Turn 5: Final summary referencing all previous turns
    await prompt(
      client,
      session.id,
      "Summarize what we learned about this project from our conversation.",
      "5",
    );

    console.log(`\n\x1b[32m✓ Multi-turn test completed\x1b[0m`);
  } finally {
    server.close();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
