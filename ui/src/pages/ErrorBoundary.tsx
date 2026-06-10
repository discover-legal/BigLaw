import { Component, type ReactNode } from "react";

/**
 * Catches render-time crashes in a section workspace so a single broken pane
 * never white-screens the whole workbench.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="empty">
          <div className="glyph">§</div>
          <h2>This workspace hit an error</h2>
          <p style={{ fontFamily: "var(--font-mono)", fontSize: 12, wordBreak: "break-word" }}>
            {this.state.error.message}
          </p>
          <button className="btn" onClick={() => this.setState({ error: null })}>Try again</button>
        </div>
      );
    }
    return this.props.children;
  }
}
