function Bar({ width, height = 10, className = "" }: { width: string; height?: number; className?: string }) {
  return <span className={`placeholder-bar ${className}`} style={{ width, height }} />;
}

export function DetailPlaceholder() {
  return <div className="detail-placeholder" role="img" aria-label="No task selected">
    <div className="placeholder-layout" aria-hidden="true">
      <div className="detail-heading">
        <div className="placeholder-title"><Bar width="68%" height={20} /></div>
        <div className="detail-badges">
          <Bar className="placeholder-select" width="96px" height={32} />
          <Bar className="placeholder-execution" width="58px" height={24} />
          <Bar className="placeholder-select placeholder-priority" width="103px" height={32} />
        </div>
      </div>
      <div className="tabs placeholder-tabs">
        <span className="placeholder-tab selected"><Bar width="48px" height={8} /></span><span className="placeholder-tab"><Bar width="40px" height={8} /></span><span className="placeholder-tab"><Bar width="40px" height={8} /></span>
      </div>
      <div className="panel-body">
        <div className="field placeholder-description">
          <span className="placeholder-label-row"><Bar width="76px" height={8} /></span>
          <div className="placeholder-lines">
            {["96%", "88%", "94%", "59%"].map(width => <Bar key={width} width={width} />)}
          </div>
        </div>
      </div>
    </div>
  </div>;
}
