function Bar({ width, height = 10 }: { width: string; height?: number }) {
  return <span className="placeholder-bar" style={{ width, height }} />;
}

export function DetailPlaceholder() {
  return <div className="detail-placeholder">
    <div className="placeholder-layout" aria-hidden="true">
      <div className="detail-heading">
        <div className="placeholder-title"><Bar width="94%" height={20} /><Bar width="64%" height={20} /></div>
        <div className="detail-badges"><Bar width="48px" height={22} /></div>
      </div>
      <div className="placeholder-move">
        <span className="placeholder-label">Move task</span>
        <div className="placeholder-control placeholder-select"><Bar width="65px" height={9} /><span className="placeholder-chevron" /></div>
      </div>
      <div className="placeholder-tabs">
        <span>Overview</span><span>Polling</span><span>History</span>
      </div>
      <div className="panel-body">
        <div className="placeholder-priority-field">
            <span className="placeholder-label">Priority</span>
            <div className="placeholder-control placeholder-priority"><Bar width="46px" height={9} /></div>
        </div>
        <div className="placeholder-description">
          <span className="placeholder-label">Description</span>
          <div className="placeholder-lines">
            {["96%", "88%", "94%", "59%"].map(width => <Bar key={width} width={width} />)}
          </div>
        </div>
        <div className="placeholder-connections">
          <h3>Connections</h3>
          <div className="placeholder-links"><Bar width="104px" height={17} /><Bar width="22px" height={17} /></div>
          <div className="placeholder-chat">
            <span className="placeholder-label">Codex chat</span>
            <div className="placeholder-control placeholder-input"><Bar width="67%" /></div>
            <div className="placeholder-help"><Bar width="85%" height={7} /></div>
            <div className="placeholder-control placeholder-button"><Bar width="51px" height={8} /></div>
          </div>
        </div>
      </div>
    </div>
    <p className="placeholder-caption">Select a card to see its details.</p>
  </div>;
}
