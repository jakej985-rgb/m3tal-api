import fs from "fs";
import { GoogleGenerativeAI } from "@google/generative-ai";

const genAI = new GoogleGenerativeAI(process.env.GOOGLE_AI_KEY);

// --- Detect M3TAL repo structure ---
function detectStructure() {
  const structure = {
    hasDocker: fs.existsSync("source/m3tal-stack/m3tal-compose.yml"),
    hasGo: fs.existsSync("source/go-backend/go.mod"),
    hasDashboard: fs.existsSync("source/dashboard/server.py"),
    hasCLI: fs.existsSync("m3tal.py"),
    services: [],
  };

  if (fs.existsSync("source")) {
    const sourceDirs = fs.readdirSync("source", { withFileTypes: true })
      .filter(d => d.isDirectory())
      .map(d => d.name);
    structure.services = sourceDirs;
  }

  return structure;
}

const structure = detectStructure();

const existingReadme = fs.existsSync("README.md")
  ? fs.readFileSync("README.md", "utf-8")
  : "";

const context = `
Repo Architectural Map:
- Orchestrator (CLI): ${structure.hasCLI ? "m3tal.py (present)" : "missing"}
- Infrastructure: ${structure.hasDocker ? "source/m3tal-stack (standardized compose)" : "missing"}
- Backend API: ${structure.hasGo ? "source/go-backend (Go native)" : "missing"}
- Dashboard: ${structure.hasDashboard ? "source/dashboard (Python/Flask)" : "missing"}
- Detected Modules: ${structure.services.join(", ") || "none"}

M3TAL Ecosystem Rules:
- If repo is part of M3tal ecosystem:
  - Add "Related Projects" section linking to m3tal-godash and m3tal-goback.
  - Explain role in system: M3tal-Media-Server is the Orchestrator/Core.
  - Emphasize path consistency (/mnt) and API-only communication.
- If Docker exists → include docker-based deployment instructions.
- If Go exists → explain the Go-native migration status.
`;

const prompt = `
You are DocSmith, the M3TAL Ecosystem Documentation Architect.

Your job:
Generate or update the README.md for this repository based on its REAL architectural layout.

STRICT RULES:
- Use real structure: Do NOT invent features or directories.
- No placeholders: Use the actual service names found in the scan.
- Relationship Mapping: Explain how the CLI, Backend, and Dashboard interact.
- Style: Professional, technical, and "Mission Control" aesthetic.
- Preservation: Keep existing valid sections but modernize them to reflect the Go-native migration.

${context}

Existing README:
${existingReadme}
`;

async function run() {
  // Try models in order of likelihood
  const modelsToTry = [
    "gemini-3-flash",          // Tier 1: Primary (5 RPM / 20 RPD)
    "gemini-3.1-flash-lite",   // Tier 2: High-Volume Fallback (15 RPM / 500 RPD)
    "gemini-2.5-flash",        // Tier 3: Secondary Fallback (5 RPM / 20 RPD)
    "gemini-2.5-flash-lite",   // Tier 3: Tertiary Fallback (10 RPM / 20 RPD)
    "gemini-1.5-flash",        // Legacy Fallback
    "gemini-pro"               // Universal Fallback
  ];
  
  for (const modelName of modelsToTry) {
    try {
      console.log(`Attempting to use model: ${modelName}`);
      const model = genAI.getGenerativeModel({ model: modelName });
      const result = await model.generateContent(prompt);
      const output = result.response.text();
      fs.writeFileSync("README.md", output);
      console.log(`README updated successfully using ${modelName}`);
      return;
    } catch (error) {
      console.warn(`Model ${modelName} failed: ${error.message}`);
    }
  }
  
  console.error("All Gemini models failed. Please check your API key and region permissions.");
  process.exit(1);
}

run();
