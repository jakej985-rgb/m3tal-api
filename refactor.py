import re
import os

filepath = "main.go"
with open(filepath, "r", encoding="utf-8") as f:
    content = f.read()

# Add getAgentLogger helper above main
helper = """
func getAgentLogger(name string) *log.Logger {
	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join("..", "state")
	}
	logsDir := filepath.Join(stateDir, "logs")
	os.MkdirAll(logsDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logsDir, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return log.New(os.Stdout, "["+name+"] ", log.LstdFlags)
	}
	return log.New(io.MultiWriter(os.Stdout, logFile), "["+name+"] ", log.LstdFlags)
}

func main() {"""
content = content.replace("func main() {", helper)

# Inject logger into each agent and replace log.Printf
agent_names = [
    "historyAgent", "apiAgent", "saveAgent", "metricsAgent", "dockerAgent", 
    "hardwareAgent", "storageAgent", "scoutAgent", "logObserverAgent", 
    "orchestratorAgent", "healerAgent", "notifyAgent", "listenerAgent"
]

for agent in agent_names:
    pattern = r'(func ' + agent + r'\([^)]*\)\s*\{)'
    def replace_agent(m):
        header = m.group(1)
        name = agent.replace('Agent', '').lower()
        return header + '\n\tlogger := getAgentLogger("' + name + '")\n\tlogger.Println("Agent started")'
    content = re.sub(pattern, replace_agent, content)

# Now manually replace the specific log calls in those functions
# historyAgent (214-264)
content = content.replace("log.Printf(\"[HISTORY]", "logger.Printf(\"")

# apiAgent
content = content.replace("log.Printf(\"❌ API AGENT:", "logger.Printf(\"❌")
content = content.replace("log.Printf(\"🚀 Control Plane API", "logger.Printf(\"🚀 Control Plane API")

# hardwareAgent
content = content.replace("log.Printf(\"[GPU]", "logger.Printf(\"")

# storageAgent
content = content.replace("log.Printf(\"[STORAGE]", "logger.Printf(\"")

# healerAgent
content = content.replace("log.Printf(\"🚨 HEALER:", "logger.Printf(\"🚨")
content = content.replace("log.Printf(\"❌ HEALER:", "logger.Printf(\"❌")

with open(filepath, "w", encoding="utf-8") as f:
    f.write(content)

print("Refactored main.go successfully")
