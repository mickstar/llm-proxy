import SwiftUI
import MenuBarCore

enum ConsoleSection: String, CaseIterable, Identifiable {
    case overview
    case traffic
    case filters
    case models
    case redactions
    case settings

    var id: String { rawValue }

    var title: String {
        switch self {
        case .overview: return "Overview"
        case .traffic: return "Traffic"
        case .filters: return "Filters"
        case .models: return "Models"
        case .redactions: return "Redactions"
        case .settings: return "Settings"
        }
    }

    var icon: String {
        switch self {
        case .overview: return "speedometer"
        case .traffic: return "arrow.left.arrow.right"
        case .filters: return "line.3.horizontal.decrease.circle"
        case .models: return "cpu"
        case .redactions: return "eye.slash"
        case .settings: return "gearshape"
        }
    }
}

struct ConsoleView: View {
    @ObservedObject var stats: StatsModel
    @ObservedObject var model: ConsoleModel
    @State private var section: ConsoleSection = .overview

    var body: some View {
        HStack(spacing: 0) {
            sidebar
                .frame(width: 196)
            Divider()
            VStack(spacing: 0) {
                header
                Divider()
                if let banner = model.banner {
                    BannerView(banner: banner) { model.banner = nil }
                        .padding(.horizontal, 16)
                        .padding(.top, 10)
                }
                detail
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(minWidth: 1040, idealWidth: 1180, maxWidth: .infinity, minHeight: 680, idealHeight: 760, maxHeight: .infinity)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 9) {
                ZStack {
                    RoundedRectangle(cornerRadius: 8, style: .continuous)
                        .fill(
                            LinearGradient(
                                colors: [Color.accentColor.opacity(0.85), Color.accentColor.opacity(0.55)],
                                startPoint: .topLeading,
                                endPoint: .bottomTrailing
                            )
                        )
                        .frame(width: 28, height: 28)
                    Image(systemName: "bolt.fill")
                        .font(.system(size: 13, weight: .bold))
                        .foregroundStyle(.white)
                }
                VStack(alignment: .leading, spacing: 0) {
                    Text("llm-proxy")
                        .font(.system(size: 13, weight: .semibold, design: .rounded))
                    Text("console")
                        .font(.system(size: 9))
                        .foregroundStyle(.tertiary)
                }
            }
            .padding(.horizontal, 8)
            .padding(.top, 14)
            .padding(.bottom, 14)

            ForEach(ConsoleSection.allCases) { item in
                Button {
                    section = item
                } label: {
                    HStack(spacing: 8) {
                        Image(systemName: item.icon)
                            .font(.system(size: 12, weight: .medium))
                            .frame(width: 17)
                        Text(item.title)
                            .font(.system(size: 12, weight: section == item ? .semibold : .regular))
                        Spacer()
                    }
                    .foregroundStyle(section == item ? Color.accentColor : Color.primary.opacity(0.82))
                    .padding(.horizontal, 9)
                    .padding(.vertical, 6)
                    .background(
                        section == item ? Color.accentColor.opacity(0.13) : Color.clear,
                        in: RoundedRectangle(cornerRadius: 7, style: .continuous)
                    )
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }

            Spacer()

            HStack(spacing: 6) {
                Circle()
                    .fill(statusColor)
                    .frame(width: 6, height: 6)
                Text(statusText)
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
                Spacer()
                if model.streaming {
                    Text("LIVE")
                        .font(.system(size: 8, weight: .bold))
                        .tracking(0.5)
                        .foregroundStyle(.green)
                }
            }
            .padding(.horizontal, 10)
            .padding(.bottom, 12)
        }
        .background(.thinMaterial)
    }

    private var header: some View {
        HStack(spacing: 10) {
            Text(section.title)
                .font(.system(size: 17, weight: .semibold))
            if section == .traffic {
                Text("\(model.traffic.count)")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if stats.snapshot?.halt.halted == true {
                Button("Resume") {
                    Task { await model.resume() }
                }
                .tint(.red)
            }
            Button {
                Task {
                    await model.reloadAll()
                    await stats.refresh()
                }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .keyboardShortcut("r", modifiers: .command)
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 11)
        .background(.bar)
    }

    @ViewBuilder
    private var detail: some View {
        switch section {
        case .overview:
            OverviewView(stats: stats, model: model)
        case .traffic:
            TrafficView(model: model)
        case .filters:
            FiltersView(model: model)
        case .models:
            ModelsView(model: model)
        case .redactions:
            RedactionsView(model: model)
        case .settings:
            SettingsView(model: model)
        }
    }

    private var statusColor: Color {
        if stats.offlineReason != nil { return .orange }
        if stats.snapshot?.halt.halted == true { return .red }
        return .green
    }

    private var statusText: String {
        if stats.offlineReason != nil { return "Offline" }
        if stats.snapshot?.halt.halted == true { return "Halted" }
        return "Running"
    }
}
