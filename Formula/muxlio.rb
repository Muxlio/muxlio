class Muxlio < Formula
  desc "Durable workflow coordinator for AI coding agents"
  homepage "https://github.com/Muxlio/muxlio"
  url "https://github.com/Muxlio/muxlio/archive/refs/tags/v0.2.0.tar.gz"
  version "0.2.0"
  sha256 "REPLACE_WITH_RELEASE_SHA256"
  license "Apache-2.0"

  depends_on "go" => :build
  depends_on "node"

  def install
    cd "muxlio-core" do
      system "go", "build", "-trimpath",
             "-ldflags", "-X main.CurrentVersion=#{version}",
             "-o", bin/"muxlio", "./cmd/muxlio"
      system "go", "build", "-trimpath",
             "-ldflags", "-X main.Version=#{version}",
             "-o", bin/"muxlio-mcp", "./cmd/muxlio-mcp"
    end

    adapters = libexec/"muxlio/adapters"
    %w[agent-claude agent-codex agent-cursor agent-gemini-cli agent-kiro command-go-test].each do |adapter|
      cd "muxlio-adapters" do
        system "go", "build", "-trimpath", "-o", adapters/adapter, "./cmd/#{adapter}"
      end
    end

    ui = share/"muxlio/ui"
    ui.install "muxlio-ui/index.html", "muxlio-ui/server.mjs", "muxlio-ui/logo.png",
                "muxlio-ui/favicon.ico", "muxlio-ui/app.css", "muxlio-ui/workspace.js", "muxlio-ui/workspace.css"
    (ui/"js").install Dir["muxlio-ui/js/*.js"]
    (share/"muxlio/skills").install "muxlio-skills/SKILL.md"
    (share/"muxlio").install "LICENSE"
  end

  test do
    assert_match "muxlio #{version}", shell_output("#{bin}/muxlio version")
    assert_path_exists libexec/"muxlio/adapters/agent-claude"
    assert_path_exists share/"muxlio/ui/index.html"
    assert_path_exists share/"muxlio/skills/SKILL.md"
  end
end
