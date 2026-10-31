function Bar({ width, height = 10 }: { width: string; height?: number }) {
  return <span className="placeholder-bar" style={{ width, height }} />;
}

export function DetailPlaceholder() {
  return <div className="detail-placeholder" role="img" aria-label="No task selected">
    <div className="placeholder-layout" aria-hidden="true">
      <div className="detail-heading">
        <div className="placeholder-title"><Bar width="94%" height={20} /><Bar width="64%" height={20} /></div>
        <div className="detail-badges"><Bar width="60px" height={26} /><Bar width="68px" height={26} /><span className="placeholder-priority"><Bar width="112px" height={26} /></span></div>
      </div>
      <div className="placeholder-tabs">
        <span>Overview</span><span>Polling</span><span>History</span>
      </div>
      <div className="panel-body">
        <div className="placeholder-description">
          <span className="placeholder-label">Description</span>
          <div className="placeholder-lines">
            {["96%", "88%", "94%", "59%"].map(width => <Bar key={width} width={width} />)}
          </div>
        </div>
      </div>
    </div>
  </div>;
}
