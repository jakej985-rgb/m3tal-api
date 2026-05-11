import fs from "fs";
import { GoogleGenerativeAI } from "@google/generative-ai";

const genAI = new GoogleGenerativeAI(process.env.GOOGLE_AI_KEY);
const model = genAI.getGenerativeModel({ model: "gemini-1.5-pro" });

const generatedReadme = fs.existsSync("README.generated.md")
  ? fs.readFileSync("README.generated.md", "utf-8")
  : "";

const prompt = `
You are DocSmith, the M3TAL Component Architect.

Task:
Polish the README for the M3TAL Go Backend (m3tal-goback).

Rules:
- Professional, technical tone.
- Explain that this is the "Brain" of the system.
- Mention the Go-native migration (moving away from Python agents).
- Keep it concise.

RAW README:
${generatedReadme}
`;

async function run() {
  const result = await model.generateContent(prompt);
  fs.writeFileSync("README.md", result.response.text());
  console.log("README polished by DocSmith for Go Backend.");
}

run();
