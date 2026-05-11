import fs from "fs";
import { GoogleGenerativeAI } from "@google/generative-ai";

const genAI = new GoogleGenerativeAI(process.env.GOOGLE_AI_KEY);
const model = genAI.getGenerativeModel({ model: "gemini-1.5-flash" });

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
  try {
    const result = await model.generateContent(prompt);
    const output = result.response.text();
    fs.writeFileSync("README_REVIEW.md", output);

    // Fail CI if blockers exist
    if (output.includes("BLOCKER")) {
      console.error("DocCritic found BLOCKER issues");
      process.exit(1);
    }

    console.log("DocCritic complete (Gemini)");
  } catch (error) {
    console.error("DocCritic failed:", error);
    process.exit(1);
  }
}

run();
