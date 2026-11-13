class Muxlio < Formula
  desc "Durable workflow coordinator for AI agents and tools"
  homepage "https://github.com/Muxlio/muxlio"
  head "https://github.com/Muxlio/muxlio.git", branch: "main"
  license "Apache-2.0"

  depends_on "go" => :build
  depends_on "node"

  def install
    system "bash", "scripts/build-release.sh", prefix.to_s
  end

  test do
    assert_match "muxlio ", shell_output("#{bin}/muxlio version")
    assert_path_exists bin/"muxlio-mcp"
    %w[agent-a2a agent-acp agent-antigravity agent-claude agent-codex agent-cursor agent-kiro command].each do |adapter|
      assert_path_exists libexec/"muxlio/adapters"/adapter
      assert_path_exists libexec/"muxlio/adapters"/"#{adapter}.json"
    end
    assert_path_exists share/"muxlio/ui/index.html"
    assert_path_exists share/"muxlio/ui/dist/app.css"
    assert_path_exists share/"muxlio/skills/SKILL.md"
    %w[echo task].each do |name|
      assert_path_exists share/"muxlio/templates/#{name}.json"
      (testpath/"#{name}.json").write shell_output("#{bin}/muxlio template #{name}")
      assert_match '"valid":true', shell_output("#{bin}/muxlio check #{name}.json --json")
    end
    (testpath/"input.json").write '{"message":"hello"}'
    assert_match '"succeeded"', shell_output("#{bin}/muxlio test echo.json --input input.json --json")
    assert_match '"succeeded"', shell_output("#{bin}/muxlio test task.json --input input.json --json")
  end
end
