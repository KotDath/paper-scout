import 'research_api.dart';

/// Immutable run snapshots and current memory entries use the same T19 Hit.
/// The original JSON retains coverage/provenance without inventing defaults.
class MemoryHit {
  MemoryHit.fromJson(this.json)
    : id = json['memory_id'] as String,
      kind = json['kind'] as String,
      title = json['title'] as String,
      text = json['text'] as String,
      revision = json['revision'] as int;

  final Json json;
  final String id, kind, title, text;
  final int revision;
  String? get targetId => json['target_id'] as String?;
  List<Json> get origins => rows(json['origins']);
  List<String> get evidenceIds =>
      (json['evidence_ids'] as List? ?? []).cast<String>();
}

class ReportMemory {
  ReportMemory.fromJson(Json json)
    : mode = json['retrieval_mode'] as String,
      reasons = (json['reasons'] as List? ?? []).cast<String>(),
      recalled = hits(json['recalled']),
      referenced = hits(json['referenced']);

  static ReportMemory? fromReport(Json report) {
    final value = report['research_memory'];
    return value == null ? null : ReportMemory.fromJson(value as Json);
  }

  static List<MemoryHit> hits(dynamic value) =>
      rows(value).map(MemoryHit.fromJson).toList();

  final String mode;
  final List<String> reasons;
  final List<MemoryHit> recalled, referenced;
}
