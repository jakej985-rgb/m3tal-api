import fs from "fs";
import { GoogleGenerativeAI } from "@google/generative-ai";

const genAI = new GoogleGenerativeAI(process.env.GOOGLE_AI_KEY);

const readme = fs.readFileSync("README.md", "utf-8");

const prompt = `
You are DocCritic, a Senior DevOps Auditor for the M3TAL platform.

Act like a NEW USER trying to run this project. Be extremely strict.

Check for:
- Missing install steps (m3tal.py setup, .env configuration).
- Missing Docker instructions (source/m3tal-stack usage).
- Missing ports / access info (Traefik gateway).
- Confusing wording or technical gaps.
- Dev-only assumptions (e.g., assuming /mnt already exists).

Classify issues:
- BLOCKER (Project cannot be deployed with this documentation)
- WARNING (Documentation is confusing or incomplete)
- SUGGESTION (Improvements for clarity)

Return:
- A clear Verdict.
- Detailed issue list with "BLOCKER", "WARNING", or "SUGGESTION" prefixes.
- Suggested fixes for every issue.

README:
${readme}
`;

async function run() {
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
      fs.writeFileSync("FIXES.md", output);

      console.log("--- DocCritic Audit Report ---");
      console.log(output);
      console.log("------------------------------");

      // Log results but do NOT fail CI (per user request: "shouldnt fail if qa has change remendations")
      if (output.includes("BLOCKER")) {
        console.warn("DocCritic found BLOCKER issues. Please review FIXES.md and address them when possible.");
        fs.writeFileSync(".doc-failed", "true");
      } else {
        console.log("DocCritic audit passed (No Blockers).");
        if (fs.existsSync(".doc-failed")) fs.unlinkSync(".doc-failed");
      }

      console.log(`DocCritic complete using ${modelName}`);
      return;
    } catch (error) {
      console.warn(`Model ${modelName} failed: ${error.message}`);
    }
  }

  console.error("All Gemini models failed. Please check your API key and region permissions.");
  process.exit(1);
}

run();
